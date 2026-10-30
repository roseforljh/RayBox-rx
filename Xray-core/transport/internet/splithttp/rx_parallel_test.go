package splithttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/protocol/tls/cert"
	"github.com/xtls/xray-core/transport/internet/stat"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
)

func TestRXParallelConfig(t *testing.T) {
	for _, tc := range []struct {
		n, segment, window int32
		valid              bool
	}{
		{0, 0, 4096, true},
		{1, 256, 4096, false},
		{-1, 256, 4096, false},
		{2, 0, 4096, false},
		{2, 256, 4096, true},
		{256, 1024, 262144, true},
		{257, 256, 262144, false},
		{8, 1024, 4096, false},
	} {
		c := rxIntegrationConfig(true)
		c.Rx.MaxConcurrentSegments, c.Rx.MaxResponseBytes, c.Rx.MaxReplayBytes = tc.n, tc.segment, tc.window
		_, err := c.NormalizeRX()
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

// Flush support lets the real handler run through a controlled writer.
type rxParallelWriter struct {
	http.ResponseWriter
	write func([]byte) (int, error)
}

func (w *rxParallelWriter) Write(b []byte) (int, error) { return w.write(b) }
func (w *rxParallelWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestRXParallelOrderingAndRepair(t *testing.T) {
	for _, fault := range []string{"none", "partial-head", "later-freeze"} {
		t.Run(fault, func(t *testing.T) {
			const size = 19*1024 + 71
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i * 31)
			}
			cfg := rxIntegrationConfig(true)
			cfg.Rx.MaxConcurrentSegments, cfg.Rx.MaxResponseBytes = 4, 1024
			cfg.Rx.RequestTimeoutMs, cfg.Rx.ProbeIntervalMs, cfg.Rx.StallTimeoutMs = 300, 20, 60
			cfg.Rx.MaxReplayBytes = 4096
			h := &requestHandler{config: cfg, sessionMu: &sync.Mutex{}, ln: &Listener{config: cfg}}
			h.ln.addConn = func(c stat.Connection) { go func() { defer c.Close(); c.Write(payload) }() }
			var injected atomic.Bool
			var repaired atomic.Bool
			var completedLater atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				action := req.Header.Get(rxActionHeader)
				offset, _ := strconv.ParseUint(req.Header.Get(rxOffsetHeader), 10, 64)
				if action == "segment" && offset == 137 {
					if req.Header.Get(rxSegmentEndHeader) != "1024" {
						t.Error("repair extended original job")
					}
					repaired.Store(true)
				}
				if action == "segment" && offset >= 2048 {
					completedLater.Store(true)
				}
				ww := &rxParallelWriter{ResponseWriter: w}
				ww.write = func(b []byte) (int, error) {
					if fault == "partial-head" && action == "open" && injected.CompareAndSwap(false, true) {
						n, err := w.Write(b[:137])
						http.NewResponseController(w).Flush()
						// Let the consumer receive the prefix while later jobs finish.
						time.Sleep(80 * time.Millisecond)
						if err == nil {
							err = io.ErrUnexpectedEOF
						}
						return n, err
					}
					if fault == "later-freeze" && action == "segment" && offset == 2048 && injected.CompareAndSwap(false, true) {
						<-req.Context().Done()
						return 0, req.Context().Err()
					}
					if action == "segment" && offset == 1024 {
						time.Sleep(10 * time.Millisecond)
					}
					return w.Write(b)
				}
				h.handleRX(ww, req, "parallel", "", nil)
			}))
			defer srv.Close()
			defer h.closeRX()
			opts, _ := cfg.NormalizeRX()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := newRXDownlink(ctx, cfg, opts, srv.URL, "parallel", func() *http.Client { return srv.Client() })
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("bytes=%d hash=%x err=%v stats=%+v", len(got), sha256.Sum256(got), err, r.statsSnapshot())
			}
			stats := r.statsSnapshot()
			if r.parallel != 4 || stats.PeakSegmentJobs > 4 || stats.PeakActiveSegments > 4 || stats.PeakPrefetchBytes > 4096 {
				t.Fatalf("bounds: %+v", stats)
			}
			if !completedLater.Load() {
				t.Fatal("no later segment requested")
			}
			if fault != "none" && (stats.ReconnectAttempts == 0 || !injected.Load()) {
				t.Fatalf("fault not repaired: %+v", stats)
			}
			if fault == "partial-head" && !repaired.Load() {
				t.Fatal("partial head repair was not observed")
			}
		})
	}
}

func TestRXParallelServerACKAndTakeover(t *testing.T) {
	var c stat.Connection
	h, srv := rxServerFixture(t, func(conn stat.Connection) { c = conn })
	h.config.Rx.MaxResponseBytes, h.config.Rx.MaxConcurrentSegments = 1024, 4
	token := strings.Repeat("ab", 32)
	request := func(action string, offset, end, ack uint64) *http.Response {
		method := "GET"
		if action == "probe" || action == "close" {
			method = "HEAD"
		}
		req, _ := http.NewRequest(method, srv.URL+"/parallel", nil)
		for k, v := range map[string]string{rxVersionHeader: "1", rxTokenHeader: token, rxActionHeader: action, rxNonceHeader: "0011223344556677", rxOffsetHeader: strconv.FormatUint(offset, 10), rxGenerationHeader: "1", rxSegmentHeader: "1024", rxParallelHeader: "4"} {
			req.Header.Set(k, v)
		}
		if action == "segment" {
			req.Header.Set(rxSegmentEndHeader, strconv.FormatUint(end, 10))
			req.Header.Set(rxAckHeader, strconv.FormatUint(ack, 10))
		}
		client := *srv.Client()
		client.Timeout = time.Second
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	open := request("open", 0, 0, 0)
	if open.StatusCode != 200 {
		t.Fatal(open.Status)
	}
	if _, err := c.Write(bytes.Repeat([]byte{7}, 3072)); err != nil {
		t.Fatal(err)
	}
	a := request("segment", 1024, 2048, 0)
	if got, err := io.ReadAll(a.Body); err != nil || len(got) != 1024 {
		t.Fatal(len(got), err)
	}
	h.sessionMu.Lock()
	s := h.rxSessions["parallel"]
	h.sessionMu.Unlock()
	s.buffer.mu.Lock()
	base := s.buffer.base
	s.buffer.mu.Unlock()
	if base != 0 {
		t.Fatal("out-of-order request ACKed unread prefix", base)
	}
	bad := request("segment", 2048, 3072, 2049)
	if bad.StatusCode != 400 {
		t.Fatal("invalid ACK accepted", bad.Status)
	}
	good := request("segment", 2048, 3072, 137)
	if got, err := io.ReadAll(good.Body); err != nil || len(got) != 1024 {
		t.Fatal(len(got), err)
	}
	// The client-side TCP for this old tail is deliberately left open.
	old := request("segment", 3072, 4096, 137)
	newer := request("segment", 3072, 4096, 137)
	if old.StatusCode != 200 || newer.StatusCode != 200 {
		t.Fatal("tail takeover", old.Status, newer.Status)
	}
	if _, err := c.Write(bytes.Repeat([]byte{8}, 1024)); err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(newer.Body); err != nil || len(got) != 1024 {
		t.Fatal("replacement tail", len(got), err)
	}
	s.buffer.mu.Lock()
	base = s.buffer.base
	s.buffer.mu.Unlock()
	if base != 137 {
		t.Fatal("ACK moved unexpectedly", base)
	}
}

func TestRXParallelRequestACKSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(rxAckHeader) != "137" {
			t.Error("ACK exceeded scheduled offset", req.Header.Get(rxAckHeader))
		}
		rxTestConfirm(w, req, 256, true)
		w.Header().Set(rxSegmentHeader, "256")
		w.Header().Set(rxParallelHeader, "2")
		w.Header().Set(rxSegmentEndHeader, "256")
	}))
	defer srv.Close()
	opts := rxTestClientOptions()
	opts.MaxResponseBytes = 256
	opts.MaxSegments = 8
	r := &rxDownlink{ctx: context.Background(), config: &Config{}, opts: opts, segment: 256, parallel: 2, offset: 200, url: srv.URL, factory: func() *http.Client { return srv.Client() }}
	response, err := r.request(context.Background(), "segment", 137, 1, false, 256)
	if err != nil {
		t.Fatal(err)
	}
	response.body.Close()
}

func TestRXParallelRepairsUnreadPrefixWithoutACK(t *testing.T) {
	const segment = 512
	payload := bytes.Repeat([]byte("0123456789abcdef"), 96)
	repaired := make(chan struct{})
	var injected atomic.Bool
	var release sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rxTestConfirm(w, req, uint64(len(payload)), true)
		w.Header().Set(rxWindowHeader, "4096")
		w.Header().Set(rxSegmentHeader, strconv.Itoa(segment))
		w.Header().Set(rxParallelHeader, "2")
		if req.Method == "HEAD" {
			return
		}
		start, _ := strconv.ParseUint(req.Header.Get(rxOffsetHeader), 10, 64)
		end := min(start+segment, uint64(len(payload)))
		if boundary := req.Header.Get(rxSegmentEndHeader); boundary != "" {
			end, _ = strconv.ParseUint(boundary, 10, 64)
		}
		w.Header().Set(rxSegmentEndHeader, strconv.FormatUint(end, 10))
		w.Header().Set("Content-Length", strconv.FormatUint(end-start, 10))
		if req.Header.Get(rxActionHeader) == "open" {
			http.NewResponseController(w).Flush()
			select {
			case <-repaired:
			case <-req.Context().Done():
				return
			}
		} else if start == segment && injected.CompareAndSwap(false, true) {
			w.Write(payload[start : start+137])
			return // The advertised length makes the client observe a partial EOF.
		} else if injected.Load() && start < 2*segment {
			if start != segment+137 || end != 2*segment || req.Header.Get(rxAckHeader) != "0" {
				t.Errorf("repair offset=%d end=%d ACK=%s; unread bytes must be retained without ACK", start, end, req.Header.Get(rxAckHeader))
			}
			release.Do(func() { close(repaired) })
		}
		w.Write(payload[start:end])
	}))
	defer srv.Close()
	opts := rxTestClientOptions()
	opts.MaxResponseBytes, opts.MaxConcurrentSegments, opts.MaxReplayBytes = segment, 2, 4096
	opts.MaxSegments = 32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := newRXDownlink(ctx, &Config{Mode: "packet-up"}, opts, srv.URL, "unread-prefix", func() *http.Client { return srv.Client() })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, payload) || !injected.Load() {
		t.Fatalf("bytes=%d err=%v injected=%v stats=%+v", len(got), err, injected.Load(), r.statsSnapshot())
	}
}

func TestRXParallelConfirmation(t *testing.T) {
	for _, mode := range []string{"old", "duplicate", "unsolicited", "changed"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				rxTestConfirm(w, req, 512, true)
				w.Header().Set(rxSegmentHeader, "256")
				offset, _ := strconv.ParseUint(req.Header.Get(rxOffsetHeader), 10, 64)
				if req.Method == "GET" {
					w.Header().Set(rxSegmentEndHeader, strconv.FormatUint(min(offset+256, 512), 10))
				}
				if mode != "old" {
					w.Header().Set(rxParallelHeader, "2")
				}
				if mode == "duplicate" {
					w.Header().Add(rxParallelHeader, "2")
				}
				if mode == "changed" && req.Header.Get(rxActionHeader) != "open" {
					w.Header().Del(rxParallelHeader)
				}
				if req.Method == "GET" {
					io.WriteString(w, strings.Repeat("x", int(min(uint64(256), 512-offset))))
				}
			}))
			defer srv.Close()
			opts := rxTestClientOptions()
			opts.MaxResponseBytes = 256
			opts.MaxSegments = 8
			opts.MaxConcurrentSegments = 2
			if mode == "unsolicited" {
				opts.MaxConcurrentSegments = 0
			}
			r, err := newRXDownlink(context.Background(), &Config{Mode: "packet-up"}, opts, srv.URL, "test", func() *http.Client { return srv.Client() })
			if mode == "duplicate" || mode == "unsolicited" {
				if err == nil {
					r.Close()
					t.Fatal("bad capability accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got, err := io.ReadAll(r)
			if mode == "old" {
				if err != nil || len(got) != 512 || r.parallel != 0 {
					t.Fatal("old fallback", len(got), err)
				}
			} else if err == nil {
				t.Fatal("lost capability accepted")
			}
		})
	}
}

func TestRXParallelIdleCancelAndBudget(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			cfg := rxIntegrationConfig(true)
			cfg.Rx.MaxResponseBytes = 256
			cfg.Rx.MaxConcurrentSegments = 2
			cfg.Rx.RequestTimeoutMs = 80
			cfg.Rx.ProbeIntervalMs = 10
			cfg.Rx.StallTimeoutMs = 30
			cfg.Rx.MaxReconnects = 2
			var conn stat.Connection
			h, srv := rxServerFixture(t, func(c stat.Connection) { conn = c })
			h.config = cfg
			h.ln.config = cfg
			if budget {
				cfg.Rx.MaxSegments = 1
			}
			opts, _ := cfg.NormalizeRX()
			r, err := newRXDownlink(context.Background(), cfg, opts, srv.URL, "test", func() *http.Client { return srv.Client() })
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if budget {
				conn.Write(bytes.Repeat([]byte{1}, 512))
				_, err = io.ReadAll(r)
				if err == nil || r.statsSnapshot().SegmentRequests != 1 {
					t.Fatal("budget unbounded", err, r.statsSnapshot())
				}
			} else {
				// A tail awaiting production outlives the normal body/header deadline.
				time.Sleep(120 * time.Millisecond)
				if r.statsSnapshot().ReconnectAttempts != 0 {
					t.Fatal("idle retried")
				}
				done := make(chan error, 1)
				go func() { _, err := r.Read(make([]byte, 1)); done <- err }()
				r.Close()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancel accepted")
					}
				case <-time.After(time.Second):
					t.Fatal("Read leaked")
				}
				select {
				case <-r.prefetch.stopped:
				case <-time.After(time.Second):
					t.Fatal("worker leaked")
				}
			}
		})
	}
}

func TestRXParallelTLS(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint(h2), func(t *testing.T) {
			sc, cc := rxIntegrationConfig(true), rxIntegrationConfig(true)
			for _, c := range []*Config{sc, cc} {
				c.Rx.MaxResponseBytes = 1024
				c.Rx.MaxConcurrentSegments = 4
				c.Rx.MaxReplayBytes = 8192
				c.Rx.RequestTimeoutMs = 2000
				c.Rx.MaxSegments = 128
				c.Rx.Preconnects = 2
			}
			const size = 32*1024 + 17
			protocol := "http/1.1"
			if h2 {
				protocol = "h2"
			}
			ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
			security := &xtls.Config{Certificate: []*xtls.Certificate{xtls.ParseCertificate(ct)}, PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{protocol}, Fingerprint: "unsafe"}
			ln := rxListen(t, sc, security, func(c stat.Connection) { defer c.Close(); rxWritePayload(c, size) })
			proxy := newRXTCPFaultProxy(t, ln.Addr().String(), "freeze", 16<<10)
			c := rxDial(t, proxy.listener.Addr().String(), cc, security)
			reader := c.(*splitConn).reader.(*rxDownlink)
			// Leave completed jobs unread long enough to queue beyond the 4 active GETs.
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if reader.statsSnapshot().PeakSegmentJobs == 8 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			rxReadHash(t, c, size)
			r := reader
			stats := r.statsSnapshot()
			if proxy.accepted.Load() < stats.SegmentRequests+stats.UploadRequests {
				t.Fatal("parallel requests reused TCP")
			}
			if stats.PeakPrefetchBytes > 8192 || stats.PeakActiveSegments > 4 {
				t.Fatal("prefetch resources unbounded", stats)
			}
			if r.parallel != 4 || r.statsSnapshot().PeakSegmentJobs != 8 {
				t.Fatal("parallel TLS bounds", r.statsSnapshot())
			}
		})
	}
}
