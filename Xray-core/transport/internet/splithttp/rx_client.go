package splithttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
)

type rxUploadContextKey struct{}

type rxClientStats struct {
	Segments           uint64
	SegmentRequests    uint64
	RequestFailures    uint64
	UploadRequests     uint64
	UploadDuration     time.Duration
	UploadRetries      uint64
	ReconnectAttempts  uint64
	Recoveries         uint64
	ConfirmedStalls    uint64
	ProbeRequests      uint64
	PeakSegmentJobs    uint64
	PeakActiveSegments uint64
	PeakPrefetchBytes  uint64
	WarmHits           uint64
	ColdDials          uint64
	RecoveryDurations  []time.Duration
}

// rxDownlink presents one ordered byte stream across exclusive HTTP transports.
// Only bytes returned by Read are acknowledged. There is no read-ahead queue.
type rxDownlink struct {
	ctx                   context.Context
	cancel                context.CancelFunc
	config                *Config
	opts                  *RXConfig
	url                   string
	sessionID             string
	token                 string
	auth                  *rxAuthCodec
	factory               func() *http.Client
	pool                  *rxPreconnect
	segment               uint64 // immutable after open negotiation
	parallel              uint64
	prefetch              *rxParallel
	nextSegment           bool
	segmentRequests       uint64
	readMu                sync.Mutex
	probeMu               sync.Mutex
	mu                    sync.Mutex
	current               *rxHTTPBody
	pending               error
	terminal              error
	offset                uint64
	ackOffset             uint64
	generation            uint64
	window                uint64
	legacy                bool
	uploadRetry           bool
	reading               bool
	readSince             time.Time
	lastProgress          time.Time
	recoveryStart         time.Time
	awaitingRecoveryBytes bool
	consecutiveReconnects uint
	retryAttempts         uint64
	stats                 rxClientStats
	ackWake               chan struct{}
	closeOnce             sync.Once
	stopContext           func() bool
	remoteAddr            net.Addr
	localAddr             net.Addr
}

// rxHTTPBody owns the entire transport, never a shared XMUX client. Closing it
// closes the TCP connection as well as the stream, including for HTTP/2.
type rxHTTPBody struct {
	io.ReadCloser
	end        uint64 // immutable final byte offset when done is true
	done       bool
	segmentEnd uint64
	segmented  bool
	start      uint64
	started    time.Time
	firstByte  bool
	cancel     context.CancelFunc
	client     *http.Client
	mu         sync.Mutex
	conn       net.Conn
	closed     bool
	once       sync.Once
}

func (b *rxHTTPBody) gotConn(info httptrace.GotConnInfo) {
	if c, ok := info.Conn.(interface{ SuppressCloseNotify() }); ok {
		c.SuppressCloseNotify()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		info.Conn.Close()
		return
	}
	b.conn = info.Conn
}

// report feeds the outcome of this request back to the destination selector.
func (b *rxHTTPBody) report(ok bool) {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		rxReportConn(conn, ok)
	}
}

func (b *rxHTTPBody) Close() error {
	b.once.Do(func() {
		b.cancel()
		b.mu.Lock()
		b.closed = true
		if b.conn != nil {
			rxCloseConn(b.conn)
		}
		b.mu.Unlock()
		if b.ReadCloser != nil {
			b.ReadCloser.Close()
		}
		b.client.CloseIdleConnections()
	})
	return nil
}

type rxResponse struct {
	body        *rxHTTPBody
	end         uint64
	done        bool
	window      uint64
	legacy      bool
	uploadRetry bool
	segment     uint64
	parallel    uint64
}

type rxProtocolError struct{ message string }

func (e *rxProtocolError) Error() string { return e.message }

func rxFatal(format string, args ...any) error {
	return &rxProtocolError{fmt.Sprintf(format, args...)}
}

func rxRandomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newRXDownlink(ctx context.Context, config *Config, opts *RXConfig, url, sessionID string, factory func() *http.Client, pools ...*rxPreconnect) (*rxDownlink, error) {
	auth, err := newRXClientAuth(opts.AuthKey, sessionID)
	if err != nil {
		return nil, err
	}
	token, err := rxRandomHex(32)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &rxDownlink{
		ctx: ctx, cancel: cancel, config: config, opts: opts, url: url, sessionID: sessionID,
		token: token, auth: auth, factory: factory, generation: 1, ackWake: make(chan struct{}, 1), lastProgress: time.Now(),
	}
	if len(pools) != 0 {
		r.pool = pools[0]
	}
	response, err := r.request(ctx, "open", 0, 1, true)
	if err != nil {
		cancel()
		if r.pool != nil {
			r.pool.close()
		}
		return nil, err
	}
	r.segment, r.parallel = response.segment, response.parallel
	if r.segment != 0 && r.pool != nil {
		r.pool.start(ctx)
	}
	r.current, r.legacy, r.window = response.body, response.legacy, response.window
	r.uploadRetry = response.uploadRetry
	response.body.mu.Lock()
	if response.body.conn != nil {
		r.remoteAddr, r.localAddr = response.body.conn.RemoteAddr(), response.body.conn.LocalAddr()
	}
	response.body.mu.Unlock()
	if r.parallel != 0 {
		r.prefetch = newRXParallel(r, response)
	}
	r.stopContext = context.AfterFunc(ctx, func() { r.fail(ctx.Err()) })
	if r.prefetch != nil {
		r.prefetch.start()
	}
	if !r.legacy {
		go r.monitor()
	} else {
		errors.LogInfo(ctx, "Xray-RX: peer did not negotiate resume; using ordinary XHTTP downlink")
	}
	return r, nil
}

func (r *rxDownlink) request(ctx context.Context, action string, offset, generation uint64, allowLegacy bool, jobEnds ...uint64) (result rxResponse, requestErr error) {
	stream := action == "open" || action == "resume" || action == "segment"
	if stream && (r.segment != 0 || (allowLegacy && r.opts.MaxResponseBytes != 0)) {
		r.mu.Lock()
		if r.segmentRequests >= uint64(r.opts.MaxSegments) {
			r.mu.Unlock()
			return rxResponse{}, rxFatal("Xray-RX segment budget exhausted")
		}
		r.segmentRequests++
		r.stats.SegmentRequests++
		r.mu.Unlock()
	}
	defer func() {
		if requestErr != nil {
			r.mu.Lock()
			r.stats.RequestFailures++
			r.mu.Unlock()
		}
	}()
	nonce, err := rxRandomHex(8)
	if err != nil {
		return rxResponse{}, err
	}
	method := http.MethodGet
	if action == "probe" || action == "close" {
		method = http.MethodHead
	}
	reqCtx, cancel := context.WithCancel(ctx)
	var client *http.Client
	if stream && r.pool != nil && (r.segment != 0 || (allowLegacy && r.opts.MaxResponseBytes != 0)) {
		client = r.pool.client()
	} else {
		ownedClient := *r.factory()
		client = &ownedClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body := &rxHTTPBody{cancel: cancel, client: client, start: offset, started: time.Now()}
	var timer *time.Timer
	reqCtx = httptrace.WithClientTrace(reqCtx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		body.gotConn(info)
		// A ready connection that cannot deliver segment headers quickly is frozen;
		// only the dial keeps the full request timeout (hedged dials cover slow handshakes).
		if action == "segment" && timer.Stop() {
			timer.Reset(min(rxSegmentResponseTimeout, time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond))
		}
		if r.opts.MaxResponseBytes != 0 {
			errors.LogInfo(ctx, "Xray-RX: request connection, action ", action, ", offset ", offset, ", generation ", generation, ", id ", rxSocketID(info.Conn))
		}
	}})
	req, err := http.NewRequestWithContext(reqCtx, method, r.url, nil)
	if err != nil {
		body.Close()
		return rxResponse{}, err
	}
	r.config.FillStreamRequest(req, r.sessionID, "")
	req.Header.Set(rxVersionHeader, "1")
	req.Header.Set(rxTokenHeader, r.token)
	req.Header.Set(rxActionHeader, action)
	req.Header.Set(rxOffsetHeader, strconv.FormatUint(offset, 10))
	req.Header.Set(rxGenerationHeader, strconv.FormatUint(generation, 10))
	req.Header.Set(rxNonceHeader, nonce)
	segment := r.segment
	if allowLegacy {
		segment = uint64(r.opts.MaxResponseBytes)
	}
	if segment != 0 {
		req.Header.Set(rxSegmentHeader, strconv.FormatUint(segment, 10))
	}
	parallel := r.parallel
	if allowLegacy {
		parallel = uint64(r.opts.MaxConcurrentSegments)
	}
	if parallel != 0 {
		req.Header.Set(rxParallelHeader, strconv.FormatUint(parallel, 10))
	}
	acknowledged := uint64(0)
	if action == "segment" {
		if len(jobEnds) != 1 {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX missing job boundary")
		}
		r.mu.Lock()
		acknowledged = min(r.offset, offset)
		r.mu.Unlock()
		req.Header.Set(rxAckHeader, strconv.FormatUint(acknowledged, 10))
		req.Header.Set(rxSegmentEndHeader, strconv.FormatUint(jobEnds[0], 10))
	}
	if action == "open" || r.uploadRetry {
		req.Header.Set(rxUploadHeader, "1")
	}
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("Accept-Encoding", "identity")
	var authCounter uint64
	if r.auth != nil {
		authCounter, err = r.auth.sealRequest(req, r.sessionID, "")
		if err != nil {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX request authentication failed")
		}
	}
	// A response-header deadline must not become the lifetime of a long body.
	timeoutDone := make(chan struct{})
	timer = time.AfterFunc(time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond, func() {
		cancel()
		close(timeoutDone)
	})
	resp, err := client.Do(req)
	if !timer.Stop() {
		<-timeoutDone
	}
	if err != nil {
		body.report(false)
		body.Close()
		return rxResponse{}, err
	}
	body.ReadCloser = resp.Body
	if r.auth != nil {
		if err := r.auth.openResponse(resp, req.Method, req.URL.EscapedPath(), r.sessionID, "", authCounter); err != nil {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX response authentication failed (HTTP %d): %v", resp.StatusCode, err)
		}
	}
	if reqCtx.Err() != nil {
		body.Close()
		return rxResponse{}, reqCtx.Err()
	}
	if resp.StatusCode != http.StatusOK {
		body.Close()
		if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return rxResponse{}, fmt.Errorf("Xray-RX temporary HTTP status %d", resp.StatusCode)
		}
		return rxResponse{}, rxFatal("Xray-RX refused HTTP status %d", resp.StatusCode)
	}
	if r.auth != nil && action == "open" {
		if !validRXHex(resp.Header.Get(rxTokenHeader), 32) {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX missing authenticated server capability")
		}
		r.token = resp.Header.Get(rxTokenHeader)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX unexpected content encoding")
	}
	hasRX := false
	for key := range resp.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-rx-") {
			hasRX = true
			break
		}
	}
	if allowLegacy && !hasRX {
		return rxResponse{body: body, legacy: true}, nil
	}
	if resp.Header.Get(rxVersionHeader) != "1" || resp.Header.Get(rxOffsetHeader) != strconv.FormatUint(offset, 10) || resp.Header.Get(rxNonceHeader) != nonce {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX missing or invalid version, offset, or nonce confirmation")
	}
	end, err := strconv.ParseUint(resp.Header.Get(rxEndHeader), 10, 64)
	if err != nil || end < offset {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX invalid produced offset")
	}
	window, err := strconv.ParseUint(resp.Header.Get(rxWindowHeader), 10, 32)
	if err != nil || window == 0 {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX invalid replay window")
	}
	done := resp.Header.Get(rxDoneHeader)
	if done != "0" && done != "1" {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX invalid completion marker")
	}
	upload := resp.Header.Get(rxUploadHeader)
	if (upload != "" && upload != "1") || (!allowLegacy && r.uploadRetry != (upload == "1")) {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX upload capability changed or invalid")
	}
	value, hasSegment := resp.Header[http.CanonicalHeaderKey(rxSegmentHeader)]
	boundary, hasBoundary := resp.Header[http.CanonicalHeaderKey(rxSegmentEndHeader)]
	accepted := uint64(0)
	if hasSegment {
		if len(value) != 1 || segment == 0 {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX unsolicited or duplicate segment confirmation")
		}
		accepted, err = strconv.ParseUint(value[0], 10, 32)
		if err != nil || accepted < 256 || accepted > segment || accepted > window {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX invalid segment budget")
		}
	}
	if (!allowLegacy && accepted != r.segment) || hasBoundary != (stream && accepted != 0) {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX segment capability changed or incomplete")
	}
	confirmedParallel := uint64(0)
	if values, present := resp.Header[http.CanonicalHeaderKey(rxParallelHeader)]; present {
		if len(values) != 1 || parallel == 0 {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX unsolicited or duplicate parallel confirmation")
		}
		confirmedParallel, err = strconv.ParseUint(values[0], 10, 32)
		if err != nil || confirmedParallel < 2 || confirmedParallel > parallel || accepted == 0 || confirmedParallel > window/accepted {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX invalid parallel confirmation")
		}
	}
	if !allowLegacy && confirmedParallel != r.parallel {
		body.Close()
		return rxResponse{}, rxFatal("Xray-RX parallel capability changed")
	}
	if hasBoundary {
		if len(boundary) != 1 || accepted > ^uint64(0)-offset {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX invalid segment boundary")
		}
		wantEnd := offset + accepted
		if action == "segment" {
			wantEnd = jobEnds[0]
			if wantEnd <= offset || wantEnd > offset+accepted {
				body.Close()
				return rxResponse{}, rxFatal("Xray-RX invalid job boundary")
			}
		}
		if done == "1" {
			wantEnd = min(wantEnd, end)
		}
		body.segmentEnd, err = strconv.ParseUint(boundary[0], 10, 64)
		if err != nil || body.segmentEnd != wantEnd {
			body.Close()
			return rxResponse{}, rxFatal("Xray-RX invalid segment end")
		}
		body.segmented = true
	}
	body.end, body.done = end, done == "1"
	if action == "segment" {
		r.mu.Lock()
		if acknowledged > r.ackOffset {
			r.ackOffset = acknowledged
		}
		r.mu.Unlock()
	}
	return rxResponse{body: body, end: end, done: done == "1", window: window, uploadRetry: upload == "1", segment: accepted, parallel: confirmedParallel}, nil
}

func (r *rxDownlink) postPacket(template *http.Request, seq string) error {
	if template.Body != nil {
		defer template.Body.Close()
	}
	if !r.uploadRetry {
		return r.postPacketAttempt(template.Context(), template, seq)
	}
	ctx, cancel := context.WithTimeout(template.Context(), time.Duration(r.opts.SessionTtlSecs)*time.Second)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	for attempt := uint(0); ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			r.mu.Lock()
			err := r.reserveRetryLocked(true)
			r.mu.Unlock()
			if err != nil {
				return err
			}
			delay := min(2*time.Second, time.Duration(r.opts.RetryDelayMs)*time.Millisecond*time.Duration(1<<min(attempt-1, uint(10))))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			errors.LogInfo(r.ctx, "Xray-RX: retrying upload, seq ", seq, ", attempt ", attempt)
		}
		err := r.postPacketAttempt(ctx, template, seq)
		if err == nil {
			if attempt > 0 {
				errors.LogInfo(r.ctx, "Xray-RX: upload confirmed after retry, seq ", seq)
			}
			return nil
		}
		if _, fatal := err.(*rxProtocolError); fatal {
			return err
		}
	}
}

func (r *rxDownlink) postPacketAttempt(ctx context.Context, template *http.Request, seq string) error {
	nonce, err := rxRandomHex(8)
	if err != nil {
		return rxFatal("Xray-RX upload nonce: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond)
	started := time.Now()
	r.mu.Lock()
	r.stats.UploadRequests++
	r.mu.Unlock()
	defer func() {
		elapsed := time.Since(started)
		r.mu.Lock()
		r.stats.UploadDuration += elapsed
		r.mu.Unlock()
		if r.opts.MaxResponseBytes != 0 {
			errors.LogInfo(ctx, "Xray-RX: upload attempt finished, seq ", seq, ", elapsed_ms ", elapsed.Milliseconds())
		}
	}()
	ownedClient := *r.factory()
	client := &ownedClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body := &rxHTTPBody{cancel: cancel, client: client}
	defer body.Close()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		body.gotConn(info)
		if r.opts.MaxResponseBytes != 0 {
			errors.LogInfo(ctx, "Xray-RX: upload connection, seq ", seq, ", id ", rxSocketID(info.Conn))
		}
	}})
	req := template.Clone(ctx)
	if template.GetBody != nil {
		req.Body, err = template.GetBody()
		if err != nil {
			return rxFatal("Xray-RX upload body: %v", err)
		}
	}
	req.Header.Set(rxVersionHeader, "1")
	if r.uploadRetry {
		req.Header.Set(rxUploadHeader, "1")
		req.Header.Set(rxNonceHeader, nonce)
	}
	req.Header.Set(rxTokenHeader, r.token)
	req.Header.Set(rxActionHeader, "upload")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("Accept-Encoding", "identity")
	var authCounter uint64
	if r.auth != nil {
		authCounter, err = r.auth.sealRequest(req, r.sessionID, seq)
		if err != nil {
			return rxFatal("Xray-RX upload authentication failed")
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	body.ReadCloser = resp.Body
	if r.auth != nil {
		if err := r.auth.openResponse(resp, req.Method, req.URL.EscapedPath(), r.sessionID, seq, authCounter); err != nil {
			return rxFatal("Xray-RX upload response authentication failed (HTTP %d): %v", resp.StatusCode, err)
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return fmt.Errorf("Xray-RX temporary upload HTTP status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return rxFatal("Xray-RX upload refused HTTP status %d", resp.StatusCode)
	}
	if !r.uploadRetry {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	if resp.Header.Get(rxVersionHeader) != "1" || resp.Header.Get(rxUploadHeader) != "1" ||
		resp.Header.Get(rxSeqHeader) != seq || resp.Header.Get(rxNonceHeader) != nonce {
		return rxFatal("Xray-RX missing or invalid upload confirmation")
	}
	return nil
}

func (r *rxDownlink) statsSnapshot() rxClientStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.RecoveryDurations = append([]time.Duration(nil), s.RecoveryDurations...)
	if r.pool != nil {
		r.pool.mu.Lock()
		s.WarmHits, s.ColdDials = r.pool.warmHits, r.pool.coldDials
		r.pool.mu.Unlock()
	}
	return s
}

func (r *rxDownlink) fail(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.terminal == nil {
		r.terminal = err
	}
	body := r.current
	r.mu.Unlock()
	if r.prefetch != nil {
		r.prefetch.close()
	}
	if body != nil {
		body.Close()
	}
	r.cancel()
	if r.pool != nil {
		r.pool.close()
	}
}

func (r *rxDownlink) probe() (rxResponse, uint64, error) {
	// Serialize ACK requests: old ACKs remain valid, but no control storms.
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	r.mu.Lock()
	offset, generation := r.offset, r.generation
	r.stats.ProbeRequests++
	r.mu.Unlock()
	response, err := r.request(r.ctx, "probe", offset, generation, false)
	if err == nil {
		response.body.Close()
		if r.prefetch != nil {
			if err := r.prefetch.observe(response); err != nil {
				return rxResponse{}, offset, err
			}
		}
		r.mu.Lock()
		if offset > r.ackOffset {
			r.ackOffset = offset
		}
		r.mu.Unlock()
	}
	return response, offset, err
}

func (r *rxDownlink) ackThresholdLocked() uint64 {
	if r.parallel != 0 {
		return max(uint64(1), min(r.window/4, r.parallel*r.segment/2))
	}
	return max(uint64(1), r.window/4)
}

func (r *rxDownlink) monitor() {
	ticker := time.NewTicker(time.Duration(r.opts.ProbeIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	var pendingSince time.Time
	var pendingOffset uint64
	var failures int
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		case <-r.ackWake:
			// A successful probe or resume may already have acknowledged the
			// bytes which queued this wake-up. Recheck actual read progress;
			// timer-driven health/idle probes remain unchanged.
			r.mu.Lock()
			needAck := r.offset-r.ackOffset >= r.ackThresholdLocked()
			r.mu.Unlock()
			if !needAck {
				continue
			}
		}
		response, ack, err := r.probe()
		if err != nil {
			if _, fatal := err.(*rxProtocolError); fatal {
				r.fail(err)
				return
			}
			failures++
		} else {
			failures = 0
		}
		if r.prefetch != nil {
			if err == nil {
				r.prefetch.detectStall()
			}
			continue
		}
		r.mu.Lock()
		if r.terminal != nil {
			r.mu.Unlock()
			return
		}
		offset, body, reading := r.offset, r.current, r.reading
		if err == nil && response.done && response.end == offset {
			r.mu.Unlock()
			r.fail(io.EOF)
			return
		}
		if err == nil && response.end > offset && reading {
			if pendingSince.IsZero() || pendingOffset != offset {
				pendingSince = time.Now()
				pendingOffset = offset
			}
		} else {
			pendingSince = time.Time{}
		}
		confirmed := r.opts.StallDetect && !pendingSince.IsZero() && time.Since(pendingSince) >= time.Duration(r.opts.StallTimeoutMs)*time.Millisecond
		unknown := r.opts.StallDetect && failures >= 2 && reading && time.Since(r.readSince) >= time.Duration(r.opts.StallTimeoutMs)*time.Millisecond
		if (confirmed || unknown) && body != nil {
			if confirmed {
				r.stats.ConfirmedStalls++
			}
			if r.recoveryStart.IsZero() {
				r.recoveryStart = time.Now()
			}
			// Clear reading until Read actually observes the cancellation; this
			// prevents repeatedly counting the same frozen response.
			r.reading = false
		}
		r.mu.Unlock()
		if (confirmed || unknown) && body != nil {
			reason := "control path unresponsive"
			if confirmed {
				reason = "pending downlink bytes stalled"
			}
			errors.LogInfo(r.ctx, "Xray-RX: ", reason, ", offset ", offset, ", acknowledged ", ack)
			body.Close()
			pendingSince = time.Time{}
		}
	}
}

func (r *rxDownlink) reserveRetryLocked(upload bool) error {
	if r.terminal != nil {
		return r.terminal
	}
	if r.retryAttempts >= uint64(r.opts.MaxReconnects) {
		return rxFatal("Xray-RX lifetime reconnect budget exhausted")
	}
	r.retryAttempts++
	if upload {
		r.stats.UploadRetries++
	} else {
		r.stats.ReconnectAttempts++
	}
	return nil
}

func (r *rxDownlink) reopen(planned bool) error {
	for {
		r.mu.Lock()
		if r.terminal != nil {
			err := r.terminal
			r.mu.Unlock()
			return err
		}
		if r.segment != 0 && r.segmentRequests >= uint64(r.opts.MaxSegments) {
			r.mu.Unlock()
			response, offset, err := r.probe()
			if err == nil && response.done && response.end == offset {
				return io.EOF
			}
			return rxFatal("Xray-RX segment budget exhausted without final confirmation")
		}
		var delay time.Duration
		if !planned {
			if err := r.reserveRetryLocked(false); err != nil {
				r.mu.Unlock()
				return err
			}
			r.consecutiveReconnects++
			delay = min(2*time.Second, time.Duration(r.opts.RetryDelayMs)*time.Millisecond*time.Duration(1<<min(r.consecutiveReconnects-1, uint(10))))
			if r.recoveryStart.IsZero() {
				r.recoveryStart = time.Now()
			}
		}
		attempt := r.stats.ReconnectAttempts
		if r.generation == ^uint64(0) {
			r.mu.Unlock()
			return rxFatal("Xray-RX generation exhausted")
		}
		r.generation++
		offset, generation := r.offset, r.generation
		r.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			// The monitor may have confirmed the final offset while this
			// retry waited. Preserve EOF (or the original terminal error).
			r.mu.Lock()
			err := r.terminal
			r.mu.Unlock()
			if err != nil {
				return err
			}
			return r.ctx.Err()
		case <-timer.C:
		}
		response, err := r.request(r.ctx, "resume", offset, generation, false)
		if err != nil {
			if _, fatal := err.(*rxProtocolError); fatal {
				return err
			}
			planned = false
			continue
		}
		r.mu.Lock()
		if r.terminal != nil {
			err := r.terminal
			r.mu.Unlock()
			response.body.Close()
			return err
		}
		// GET resume carries the ACK itself. Its confirmed final snapshot also
		// proves completion without an extra HEAD before/after the GET.
		if offset > r.ackOffset {
			r.ackOffset = offset
		}
		if response.done && response.end == offset {
			r.mu.Unlock()
			response.body.Close()
			return io.EOF
		}
		r.current, r.window = response.body, response.window
		r.awaitingRecoveryBytes = !planned
		r.mu.Unlock()
		if !planned {
			errors.LogInfo(r.ctx, "Xray-RX: reopened downlink, offset ", offset, ", attempt ", attempt)
		}
		return nil
	}
}

func (r *rxDownlink) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.prefetch != nil {
		return r.prefetch.read(p)
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	for {
		r.mu.Lock()
		if r.terminal != nil {
			err := r.terminal
			r.mu.Unlock()
			return 0, err
		}
		body, pending, planned := r.current, r.pending, r.nextSegment
		r.pending, r.nextSegment = nil, false
		r.reading, r.readSince = true, time.Now()
		r.mu.Unlock()
		if body == nil {
			if err := r.reopen(planned); err != nil {
				r.fail(err)
				return 0, err
			}
			continue
		}
		var n int
		err := pending
		if err == nil {
			q := p
			if body.segmented {
				r.mu.Lock()
				remaining := body.segmentEnd - r.offset
				r.mu.Unlock()
				q = p[:min(uint64(len(p)), remaining)]
			}
			if len(q) == 0 {
				err = io.EOF
			} else {
				n, err = body.Read(q)
			}
		}
		r.mu.Lock()
		r.reading = false
		if n > 0 {
			r.offset += uint64(n)
			r.consecutiveReconnects = 0
			r.lastProgress = time.Now()
			r.pending = err
			if r.awaitingRecoveryBytes && !r.recoveryStart.IsZero() {
				duration := time.Since(r.recoveryStart)
				r.stats.Recoveries++
				// ponytail: retain a bounded prefix of fault samples, never one per normal segment.
				if len(r.stats.RecoveryDurations) < 1024 {
					r.stats.RecoveryDurations = append(r.stats.RecoveryDurations, duration)
				}
				r.recoveryStart = time.Time{}
				r.awaitingRecoveryBytes = false
				errors.LogInfo(r.ctx, "Xray-RX: first resumed bytes delivered, offset ", r.offset, ", recovery_ms ", duration.Milliseconds())
			}
			boundary := body.segmented && r.offset == body.segmentEnd && (err == nil || err == io.EOF)
			if !body.firstByte && body.segmented {
				body.firstByte = true
				errors.LogInfo(r.ctx, "Xray-RX: segment first byte, start ", body.start, ", end ", body.segmentEnd, ", ttfb_ms ", time.Since(body.started).Milliseconds())
			}
			if boundary {
				r.current, r.pending, r.nextSegment = nil, nil, true
				r.stats.Segments++
				if body.done && body.end == r.offset && r.terminal == nil {
					r.terminal = io.EOF
				}
				errors.LogInfo(r.ctx, "Xray-RX: segment end, start ", body.start, ", end ", body.segmentEnd, ", delivered ", r.offset-body.start, ", reason budget")
			}
			finished := boundary && body.done && body.end == r.offset
			wake := !r.legacy && r.offset-r.ackOffset >= max(uint64(1), r.window/4)
			r.mu.Unlock()
			if boundary {
				body.Close()
			}
			if finished {
				r.fail(io.EOF)
			}
			if wake {
				select {
				case r.ackWake <- struct{}{}:
				default:
				}
			}
			return n, nil
		}
		if r.terminal != nil {
			terminal := r.terminal
			r.mu.Unlock()
			return 0, terminal
		}
		if err == nil {
			r.mu.Unlock()
			continue
		}
		legacy := r.legacy
		finished := body.done && body.end == r.offset
		if r.current == body {
			r.current = nil
		}
		if !finished && r.recoveryStart.IsZero() {
			r.recoveryStart = time.Now()
		}
		offset := r.offset
		r.mu.Unlock()
		body.Close()
		if body.segmented {
			errors.LogInfo(r.ctx, "Xray-RX: segment end, start ", body.start, ", end ", body.segmentEnd, ", delivered ", offset-body.start, ", reason ", err)
		}
		if legacy {
			r.fail(err)
			return 0, err
		}
		if body.segmented && err == io.EOF && !finished {
			response, ack, probeErr := r.probe()
			if probeErr == nil && response.done && response.end == ack {
				finished = true
			} else if _, fatal := probeErr.(*rxProtocolError); fatal {
				r.fail(probeErr)
				return 0, probeErr
			} else {
				r.mu.Lock()
				exhausted := r.segmentRequests >= uint64(r.opts.MaxSegments)
				r.mu.Unlock()
				if exhausted {
					err := rxFatal("Xray-RX segment budget exhausted without final confirmation")
					r.fail(err)
					return 0, err
				}
			}
		}
		if finished {
			if body.segmented {
				r.mu.Lock()
				r.stats.Segments++
				r.mu.Unlock()
			}
			r.fail(io.EOF)
			return 0, io.EOF
		}
		if err := r.reopen(false); err != nil {
			r.fail(err)
			return 0, err
		}
	}
}

func (r *rxDownlink) Close() error {
	r.closeOnce.Do(func() {
		if r.stopContext != nil {
			r.stopContext()
		}
		r.fail(io.ErrClosedPipe)
		if r.prefetch != nil {
			timer := time.NewTimer(time.Duration(r.opts.RequestTimeoutMs) * time.Millisecond)
			select {
			case <-r.prefetch.stopped:
				timer.Stop()
			case <-timer.C:
			}
			s := r.statsSnapshot()
			errors.LogInfo(r.ctx, "Xray-RX: parallel summary, peak_jobs ", s.PeakSegmentJobs, ", peak_active ", s.PeakActiveSegments, ", peak_buffered ", s.PeakPrefetchBytes, ", warm_hits ", s.WarmHits, ", cold_dials ", s.ColdDials)
		}
		if r.legacy {
			return
		}
		r.mu.Lock()
		offset, generation := r.offset, r.generation
		r.mu.Unlock()
		// Keep the instance values: embedders resolve the protected dialer via core.FromContext.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), min(250*time.Millisecond, time.Duration(r.opts.RequestTimeoutMs)*time.Millisecond))
		defer cancel()
		if response, err := r.request(ctx, "close", offset, generation, false); err == nil {
			response.body.Close()
		}
	})
	return nil
}
