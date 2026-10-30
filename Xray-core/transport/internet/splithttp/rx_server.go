package splithttp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/transport/internet/stat"
)

const (
	rxVersionHeader    = "X-RX-Version"
	rxTokenHeader      = "X-RX-Token"
	rxOffsetHeader     = "X-RX-Offset"
	rxGenerationHeader = "X-RX-Generation"
	rxActionHeader     = "X-RX-Action"
	rxNonceHeader      = "X-RX-Nonce"
	rxEndHeader        = "X-RX-End"
	rxDoneHeader       = "X-RX-Done"
	rxWindowHeader     = "X-RX-Window"
	rxUploadHeader     = "X-RX-Upload"
	rxSeqHeader        = "X-RX-Seq"
	rxSegmentHeader    = "X-RX-Segment"
	rxSegmentEndHeader = "X-RX-Segment-End"
	rxParallelHeader   = "X-RX-Parallel"
	rxAckHeader        = "X-RX-Ack"
)

type rxActiveSegment struct {
	start, end uint64
	cancel     context.CancelFunc
	done       chan struct{}
	replacing  bool
}

// Session fields are guarded by sessionMu, except the immutable uploadRetry,
// uploadSlot-protected upload cursor/hash, and independently synchronized buffer/queue/slots.
type rxSession struct {
	legacy     *httpSession
	buffer     *rxBuffer
	token      string
	generation uint64
	lastSeen   time.Time
	timer      *time.Timer
	cancel     context.CancelFunc
	slots      chan struct{}
	started    bool
	segment    uint64 // immutable negotiated application-byte budget
	segments   uint64
	parallel   uint64
	active     map[*rxActiveSegment]struct{}

	uploadRetry   bool
	uploadSlot    chan struct{}
	nextUploadSeq uint64
	uploadHash    [sha256.Size]byte
}

func (s *rxSession) cancelDownlinks() {
	if s.cancel != nil {
		s.cancel()
	}
	for a := range s.active {
		a.cancel()
	}
}

func validRXHex(value string, size int) bool {
	if len(value) != size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (h *requestHandler) expireRX(id string, s *rxSession, ttl time.Duration) {
	h.sessionMu.Lock()
	if h.rxSessions[id] != s {
		h.sessionMu.Unlock()
		return
	}
	remaining := ttl - time.Since(s.lastSeen)
	if remaining > 0 {
		s.timer.Reset(remaining)
		h.sessionMu.Unlock()
		return
	}
	delete(h.rxSessions, id)
	s.cancelDownlinks()
	h.sessionMu.Unlock()
	s.buffer.abort()
	s.legacy.uploadQueue.Close()
}

func (h *requestHandler) closeRX() {
	h.sessionMu.Lock()
	h.rxClosed = true
	sessions := h.rxSessions
	h.rxSessions = nil
	for _, s := range sessions {
		s.timer.Stop()
		s.cancelDownlinks()
	}
	h.sessionMu.Unlock()
	for _, s := range sessions {
		s.buffer.abort()
		s.legacy.uploadQueue.Close()
	}
}

// handleRX intercepts only negotiated requests or access to an RX-owned ID.
// A non-nil session with handled=false authorizes the ordinary packet-up path.
func (h *requestHandler) handleRX(w http.ResponseWriter, r *http.Request, id, seq string, remote net.Addr) (*httpSession, bool) {
	if !h.config.GetRx().GetEnabled() {
		return nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	h.sessionMu.Lock()
	s := h.rxSessions[id]
	if r.Header.Get(rxVersionHeader) == "" && s == nil {
		h.sessionMu.Unlock()
		return nil, false
	}
	fail := func(code int) (*httpSession, bool) { h.sessionMu.Unlock(); w.WriteHeader(code); return nil, true }
	if h.rxClosed {
		return fail(http.StatusServiceUnavailable)
	}
	if r.Header.Get(rxVersionHeader) != "1" || id == "" || len(id) > 256 {
		return fail(http.StatusBadRequest)
	}
	token := r.Header.Get(rxTokenHeader)
	if !validRXHex(token, 32) {
		return fail(http.StatusForbidden)
	}
	if s != nil && subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		return fail(http.StatusForbidden)
	}
	opts, err := h.config.normalizedRX()
	if err != nil {
		return fail(http.StatusBadRequest)
	}
	action := r.Header.Get(rxActionHeader)
	upload := r.Header.Get(rxUploadHeader)
	if upload != "" && upload != "1" {
		return fail(http.StatusBadRequest)
	}
	if s != nil && s.uploadRetry != (upload == "1") {
		return fail(http.StatusConflict)
	}
	if action == "upload" {
		if s == nil {
			return fail(http.StatusGone)
		}
		if seq == "" || (r.Method != "GET" && r.Method != h.config.GetNormalizedUplinkHTTPMethod()) ||
			(s.uploadRetry && !validRXHex(r.Header.Get(rxNonceHeader), 8)) {
			return fail(http.StatusBadRequest)
		}
		s.lastSeen = time.Now()
		s.timer.Reset(time.Duration(opts.SessionTtlSecs) * time.Second)
		h.sessionMu.Unlock()
		return s.legacy, false
	}
	if seq != "" || !validRXHex(r.Header.Get(rxNonceHeader), 8) {
		return fail(http.StatusBadRequest)
	}
	offset, err := strconv.ParseUint(r.Header.Get(rxOffsetHeader), 10, 64)
	if err != nil {
		return fail(http.StatusBadRequest)
	}
	gen, err := strconv.ParseUint(r.Header.Get(rxGenerationHeader), 10, 64)
	if err != nil || gen == 0 {
		return fail(http.StatusBadRequest)
	}
	if (action == "open" || action == "resume" || action == "segment") && r.Method != "GET" {
		return fail(http.StatusBadRequest)
	}
	if (action == "probe" || action == "close") && r.Method != "HEAD" {
		return fail(http.StatusBadRequest)
	}
	if action != "open" && action != "resume" && action != "segment" && action != "probe" && action != "close" {
		return fail(http.StatusBadRequest)
	}
	var segment uint64
	if value, present := r.Header[http.CanonicalHeaderKey(rxSegmentHeader)]; present {
		if len(value) != 1 {
			return fail(http.StatusBadRequest)
		}
		segment, err = strconv.ParseUint(value[0], 10, 32)
		if err != nil || segment < 256 || segment > 65536 {
			return fail(http.StatusBadRequest)
		}
	}
	parallel, boundary, explicitAck := uint64(0), uint64(0), uint64(0)
	parseHeader := func(key string) (uint64, bool, bool) {
		v, present := r.Header[http.CanonicalHeaderKey(key)]
		if !present {
			return 0, false, true
		}
		if len(v) != 1 {
			return 0, true, false
		}
		n, err := strconv.ParseUint(v[0], 10, 64)
		return n, true, err == nil
	}
	parallel, hasParallel, ok := parseHeader(rxParallelHeader)
	if !ok || (hasParallel && (parallel < 2 || parallel > 256)) {
		return fail(http.StatusBadRequest)
	}
	boundary, hasBoundary, ok := parseHeader(rxSegmentEndHeader)
	if !ok {
		return fail(http.StatusBadRequest)
	}
	explicitAck, hasAck, ok := parseHeader(rxAckHeader)
	if !ok {
		return fail(http.StatusBadRequest)
	}
	if s != nil {
		if segment != s.segment {
			return fail(http.StatusBadRequest)
		}
	} else {
		segment = min(segment, uint64(opts.MaxResponseBytes))
	}
	if segment > ^uint64(0)-offset {
		return fail(http.StatusBadRequest)
	}
	if s == nil {
		if hasBoundary || hasAck {
			return fail(http.StatusBadRequest)
		}
		parallel = min(parallel, uint64(opts.MaxConcurrentSegments))
		if segment == 0 {
			parallel = 0
		} else {
			parallel = min(parallel, uint64(opts.MaxReplayBytes)/segment)
		}
		if parallel < 2 {
			parallel = 0
		}
	} else {
		if parallel != s.parallel || (s.parallel != 0 && (gen != 1 || action == "resume")) || (action == "segment" && s.parallel == 0) {
			return fail(http.StatusBadRequest)
		}
		if action == "segment" {
			if !hasBoundary || !hasAck || boundary <= offset || boundary > offset+segment || explicitAck > offset {
				return fail(http.StatusBadRequest)
			}
		} else if hasBoundary || hasAck {
			return fail(http.StatusBadRequest)
		}
	}
	if s == nil {
		if action != "open" {
			return fail(http.StatusGone)
		}
		if offset != 0 || gen != 1 {
			return fail(http.StatusBadRequest)
		}
		if _, exists := h.sessions.Load(id); exists {
			return fail(http.StatusConflict)
		}
		if len(h.rxSessions) >= int(opts.MaxSessions) {
			return fail(http.StatusTooManyRequests)
		}
		if h.rxSessions == nil {
			h.rxSessions = make(map[string]*rxSession)
		}
		s = &rxSession{
			legacy: &httpSession{uploadQueue: NewUploadQueue(h.config.GetNormalizedScMaxBufferedPosts()), isFullyConnected: done.New()},
			buffer: newRXBuffer(int(opts.MaxReplayBytes)), token: token, lastSeen: time.Now(), slots: make(chan struct{}, 2),
			uploadRetry: upload == "1", uploadSlot: make(chan struct{}, 1), segment: segment,
		}
		if parallel != 0 {
			s.slots = make(chan struct{}, int(parallel))
			s.parallel = parallel
			s.active = make(map[*rxActiveSegment]struct{})
		}
		s.legacy.rx = s
		h.rxSessions[id] = s
		ttl := time.Duration(opts.SessionTtlSecs) * time.Second
		s.timer = time.AfterFunc(ttl, func() { h.expireRX(id, s, ttl) })
	}
	if action == "open" && s.started {
		return fail(http.StatusConflict)
	}
	if action == "resume" && gen <= s.generation {
		return fail(http.StatusConflict)
	}
	if (action == "probe" || action == "close") && gen > s.generation {
		return fail(http.StatusConflict)
	}
	if action == "close" && gen != s.generation {
		return fail(http.StatusConflict)
	}
	stream := action == "open" || action == "resume" || action == "segment"
	requestStarted := time.Now()
	logStage := func(stage string) {
		errors.LogInfo(r.Context(), "Xray-RX: server stage ", stage, ", action ", action, ", generation ", gen, ", offset ", offset, ", elapsed_ms ", time.Since(requestStarted).Milliseconds())
	}
	if stream {
		if s.segment != 0 {
			if s.segments >= uint64(opts.MaxSegments) {
				return fail(http.StatusForbidden)
			}
			s.segments++
		}
		if s.parallel != 0 {
			end, _, err := s.buffer.state(offset, false)
			if err != nil || (action == "segment" && explicitAck > end) {
				return fail(http.StatusGone)
			}
			requestEnd := offset + s.segment
			if hasBoundary {
				requestEnd = boundary
			}
			var replaced []*rxActiveSegment
			for a := range s.active {
				if offset < a.end && a.start < requestEnd {
					if a.replacing {
						return fail(http.StatusTooManyRequests)
					}
					replaced = append(replaced, a)
				}
			}
			for _, a := range replaced {
				a.replacing = true
				a.cancel()
			}
			if len(replaced) != 0 {
				h.sessionMu.Unlock()
				ctx, stop := context.WithTimeout(r.Context(), time.Duration(opts.RequestTimeoutMs)*time.Millisecond)
				for _, a := range replaced {
					select {
					case <-a.done:
					case <-ctx.Done():
					}
				}
				waitErr := ctx.Err()
				stop()
				h.sessionMu.Lock()
				for _, a := range replaced {
					a.replacing = false
				}
				if waitErr != nil {
					return fail(http.StatusServiceUnavailable)
				}
				if h.rxSessions[id] != s || h.rxClosed {
					return fail(http.StatusGone)
				}
				for a := range s.active {
					if offset < a.end && a.start < requestEnd {
						return fail(http.StatusTooManyRequests)
					}
				}
			}
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return fail(http.StatusTooManyRequests)
		}
	}
	// A stale ACK may arrive after a newer ACK; it must not move base backwards.
	ack := action == "probe" || action == "close"
	end, finished, err := s.buffer.state(offset, ack)
	if err != nil {
		if stream {
			<-s.slots
		}
		return fail(http.StatusGone)
	}
	if !ack {
		acknowledged := offset
		if s.parallel != 0 {
			acknowledged = explicitAck
		}
		end, finished, err = s.buffer.state(acknowledged, true)
		if err != nil {
			if stream {
				<-s.slots
			}
			return fail(http.StatusGone)
		}
	}
	if action != "probe" || gen == s.generation {
		s.lastSeen = time.Now()
		s.timer.Reset(time.Duration(opts.SessionTtlSecs) * time.Second)
	}
	var streamCtx context.Context
	var cancel context.CancelFunc
	start := false
	var active *rxActiveSegment
	if stream {
		if s.parallel == 0 {
			s.cancelDownlinks()
		}
		streamCtx, cancel = context.WithCancel(r.Context())
		if s.parallel != 0 {
			requestEnd := offset + s.segment
			if hasBoundary {
				requestEnd = boundary
			}
			active = &rxActiveSegment{start: offset, end: requestEnd, cancel: cancel, done: make(chan struct{})}
			s.active[active] = struct{}{}
		} else {
			s.cancel = cancel
		}
		s.generation = gen
		start = !s.started
		s.started = true
	}
	if action == "close" {
		delete(h.rxSessions, id)
		s.timer.Stop()
		s.cancelDownlinks()
	}
	segmentEnd := offset + s.segment
	if hasBoundary {
		segmentEnd = boundary
	}
	if finished && s.segment != 0 {
		segmentEnd = min(segmentEnd, end)
	}
	h.sessionMu.Unlock()
	if stream {
		logStage("authorized")
	}
	if s.parallel != 0 {
		w.Header().Set(rxParallelHeader, strconv.FormatUint(s.parallel, 10))
	}
	if s.segment != 0 {
		w.Header().Set(rxSegmentHeader, strconv.FormatUint(s.segment, 10))
		if stream {
			w.Header().Set(rxSegmentEndHeader, strconv.FormatUint(segmentEnd, 10))
		}
	}
	w.Header().Set(rxVersionHeader, "1")
	if s.uploadRetry {
		w.Header().Set(rxUploadHeader, "1")
	}
	w.Header().Set(rxOffsetHeader, strconv.FormatUint(offset, 10))
	w.Header().Set(rxNonceHeader, r.Header.Get(rxNonceHeader))
	w.Header().Set(rxEndHeader, strconv.FormatUint(end, 10))
	w.Header().Set(rxWindowHeader, strconv.Itoa(int(opts.MaxReplayBytes)))
	w.Header().Set(rxDoneHeader, "0")
	if finished {
		w.Header().Set(rxDoneHeader, "1")
	}
	w.Header().Set("X-Accel-Buffering", "no")
	if action == "close" {
		s.buffer.abort()
		s.legacy.uploadQueue.Close()
		w.WriteHeader(http.StatusOK)
		return nil, true
	}
	if action == "probe" {
		w.WriteHeader(http.StatusOK)
		return nil, true
	}
	defer cancel()
	defer func() {
		h.sessionMu.Lock()
		if active != nil {
			delete(s.active, active)
			close(active.done)
		}
		<-s.slots
		h.sessionMu.Unlock()
	}()
	controller := http.NewResponseController(w)
	var deadlineMu sync.Mutex
	setDeadline := func(deadline time.Time) {
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		if streamCtx.Err() != nil {
			deadline = time.Now()
		}
		controller.SetWriteDeadline(deadline)
	}
	// Interrupt a blocked write when a newer generation replaces this GET.
	deadlineDone := make(chan struct{})
	stop := context.AfterFunc(streamCtx, func() { setDeadline(time.Now()); close(deadlineDone) })
	defer func() {
		if !stop() {
			<-deadlineDone
		}
		controller.SetWriteDeadline(time.Time{})
	}()
	if !h.config.NoSSEHeader {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	setDeadline(time.Now().Add(time.Duration(opts.RequestTimeoutMs) * time.Millisecond))
	if start {
		local := h.localAddr
		if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && addr != nil {
			local = addr
		}
		conn := &splitConn{reader: s.legacy.uploadQueue, writer: s.buffer, remoteAddr: remote, localAddr: local}
		h.ln.addConn(stat.Connection(conn))
	}
	logStage("write-header")
	w.WriteHeader(http.StatusOK)
	logStage("write-header-returned")
	if err := controller.Flush(); err != nil {
		logStage("header-flush-failed")
		return nil, true
	}
	logStage("header-flushed")
	setDeadline(time.Time{})
	firstData := true
	logStage("waiting-first-data")
	for {
		if s.segment != 0 && offset == segmentEnd {
			if firstData {
				logStage("final-empty-segment")
			}
			return nil, true
		}
		p, err := s.buffer.read(streamCtx, offset)
		if err != nil {
			if firstData {
				switch {
				case err == io.EOF:
					logStage("first-data-eof")
				case streamCtx.Err() != nil:
					logStage("first-data-canceled")
				default:
					logStage("first-data-unavailable")
				}
			}
			return nil, true
		}
		if firstData {
			logStage("first-data-ready")
			firstData = false
		}
		if s.segment != 0 && uint64(len(p)) > segmentEnd-offset {
			p = p[:segmentEnd-offset]
		}
		setDeadline(time.Now().Add(time.Duration(opts.RequestTimeoutMs) * time.Millisecond))
		n, err := w.Write(p)
		offset += uint64(n)
		if err != nil || n != len(p) {
			return nil, true
		}
		if err := controller.Flush(); err != nil {
			return nil, true
		}
		setDeadline(time.Time{})
		if streamCtx.Err() != nil {
			return nil, true
		}
	}
}

func (h *requestHandler) pushRXUpload(w http.ResponseWriter, r *http.Request, s *rxSession, seq uint64, payload []byte) {
	opts, err := h.config.normalizedRX()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(opts.RequestTimeoutMs)*time.Millisecond)
	defer cancel()
	select {
	case s.uploadSlot <- struct{}{}:
		defer func() { <-s.uploadSlot }()
	case <-ctx.Done():
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if ctx.Err() != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	hash := sha256.Sum256(payload)
	if s.nextUploadSeq > 0 && seq == s.nextUploadSeq-1 {
		if hash != s.uploadHash {
			w.WriteHeader(http.StatusConflict)
			return
		}
	} else {
		if seq != s.nextUploadSeq || seq == ^uint64(0) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if err := s.legacy.uploadQueue.push(ctx, Packet{Payload: payload, Seq: seq}); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.nextUploadSeq++
		s.uploadHash = hash
	}
	// ponytail: stop-and-wait needs only the last accepted hash; a concurrent
	// upload window would require a bounded per-sequence acceptance map.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(rxVersionHeader, "1")
	w.Header().Set(rxUploadHeader, "1")
	w.Header().Set(rxSeqHeader, strconv.FormatUint(seq, 10))
	w.Header().Set(rxNonceHeader, r.Header.Get(rxNonceHeader))
	w.WriteHeader(http.StatusOK)
}

var _ io.WriteCloser = (*rxBuffer)(nil)
