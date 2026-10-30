package splithttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/transport/internet/stat"
)

func rxServerFixture(t *testing.T, handler func(stat.Connection), http2 ...bool) (*requestHandler, *httptest.Server) {
	t.Helper()
	cfg := &Config{Mode: "packet-up", Rx: &RXConfig{Enabled: true, MaxSessions: 1}}
	h := &requestHandler{config: cfg, sessionMu: &sync.Mutex{}, ln: &Listener{config: cfg, addConn: handler}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, handled := h.handleRX(w, r, strings.TrimPrefix(r.URL.Path, "/"), "", nil)
		if !handled {
			w.WriteHeader(418)
		}
	}))
	if len(http2) > 0 && http2[0] {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(func() { h.closeRX(); srv.Close() })
	return h, srv
}

func rxTestRequest(t *testing.T, srv *httptest.Server, id, action, token string, offset, gen uint64) *http.Response {
	t.Helper()
	method := "GET"
	if action == "probe" || action == "close" {
		method = "HEAD"
	}
	req, err := http.NewRequest(method, srv.URL+"/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(rxVersionHeader, "1")
	req.Header.Set(rxTokenHeader, token)
	req.Header.Set(rxOffsetHeader, strconv.FormatUint(offset, 10))
	req.Header.Set(rxGenerationHeader, strconv.FormatUint(gen, 10))
	req.Header.Set(rxActionHeader, action)
	req.Header.Set(rxNonceHeader, "0011223344556677")
	client := *srv.Client()
	client.Timeout = 2 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type rxFirstFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	first  int
}

func (w *rxFirstFlushRecorder) Flush() {
	if w.first < 0 {
		w.first = w.Body.Len()
	}
	w.ResponseRecorder.Flush()
	w.cancel()
}

func TestRXServerSegmentFirstFlush(t *testing.T) {
	for _, tc := range []struct {
		name     string
		segment  int32
		parallel int32
		produced int
		first    int
	}{
		{"ready_resume", 256, 0, 512, 256},
		{"pending_resume", 256, 0, 384, 0},
		{"ready_parallel", 256, 2, 512, 256},
		{"pending_parallel", 256, 2, 384, 0},
		{"legacy", 0, 0, 512, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Mode: "packet-up", Rx: &RXConfig{Enabled: true,
				MaxResponseBytes: tc.segment, MaxConcurrentSegments: tc.parallel}}
			h := &requestHandler{config: cfg, sessionMu: &sync.Mutex{}, ln: &Listener{config: cfg,
				addConn: func(c stat.Connection) {
					if _, err := c.Write([]byte(strings.Repeat("x", tc.produced))); err != nil {
						t.Fatal(err)
					}
				}}}
			t.Cleanup(h.closeRX)
			request := func(action string, offset, generation uint64) *rxFirstFlushRecorder {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				r := httptest.NewRequest("GET", "https://localhost/flush", nil).WithContext(ctx)
				r.Header.Set(rxVersionHeader, "1")
				r.Header.Set(rxTokenHeader, strings.Repeat("ab", 32))
				r.Header.Set(rxNonceHeader, "0011223344556677")
				r.Header.Set(rxActionHeader, action)
				r.Header.Set(rxOffsetHeader, strconv.FormatUint(offset, 10))
				r.Header.Set(rxGenerationHeader, strconv.FormatUint(generation, 10))
				if tc.segment != 0 {
					r.Header.Set(rxSegmentHeader, strconv.Itoa(int(tc.segment)))
				}
				if tc.parallel != 0 {
					r.Header.Set(rxParallelHeader, strconv.Itoa(int(tc.parallel)))
					if action == "segment" {
						r.Header.Set(rxSegmentEndHeader, "512")
						r.Header.Set(rxAckHeader, "256")
					}
				}
				w := &rxFirstFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, first: -1}
				h.handleRX(w, r, "flush", "", nil)
				if w.Code != http.StatusOK || w.Header().Get(rxNonceHeader) != "0011223344556677" {
					t.Fatalf("invalid response: status=%d", w.Code)
				}
				return w
			}
			if w := request("open", 0, 1); w.first != 0 {
				t.Fatalf("open delayed headers: first flush bytes=%d", w.first)
			}
			action, generation := "resume", uint64(2)
			if tc.parallel != 0 {
				action, generation = "segment", 1
			}
			w := request(action, 256, generation)
			if w.first != tc.first {
				t.Fatalf("first flush bytes=%d, want %d", w.first, tc.first)
			}
		})
	}
}

func TestRXServerSegmentBounds(t *testing.T) {
	h, srv := rxServerFixture(t, func(c stat.Connection) { go func() { c.Write([]byte(strings.Repeat("x", 512))) }() })
	h.config.Rx.MaxResponseBytes, h.config.Rx.MaxSegments = 256, 1
	token := strings.Repeat("ab", 32)
	request := func(action, budget string, offset, generation uint64) *http.Response {
		method := "GET"
		if action == "probe" || action == "close" {
			method = "HEAD"
		}
		req, _ := http.NewRequest(method, srv.URL+"/segment", nil)
		req.Header.Set(rxVersionHeader, "1")
		req.Header.Set(rxTokenHeader, token)
		req.Header.Set(rxActionHeader, action)
		req.Header.Set(rxOffsetHeader, strconv.FormatUint(offset, 10))
		req.Header.Set(rxGenerationHeader, strconv.FormatUint(generation, 10))
		req.Header.Set(rxNonceHeader, "0011223344556677")
		if budget != "" {
			req.Header.Set(rxSegmentHeader, budget)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	resp := request("open", "1024", 0, 1)
	p, err := io.ReadAll(resp.Body)
	if err != nil || len(p) != 256 || resp.Header.Get(rxSegmentHeader) != "256" || resp.Header.Get(rxSegmentEndHeader) != "256" {
		t.Fatalf("server cap: bytes=%d err=%v headers=%v", len(p), err, resp.Header)
	}
	for _, tc := range []struct {
		budget string
		offset uint64
		status int
	}{
		{"512", 256, 400},
		{"", 256, 400},
		{"256", ^uint64(0) - 127, 400},
		{"256", 256, 403},
	} {
		if resp := request("resume", tc.budget, tc.offset, 2); resp.StatusCode != tc.status {
			t.Fatalf("budget=%q offset=%d status=%d", tc.budget, tc.offset, resp.StatusCode)
		}
	}
	h.sessionMu.Lock()
	s := h.rxSessions["segment"]
	generation, segments := s.generation, s.segments
	h.sessionMu.Unlock()
	s.buffer.mu.Lock()
	base := s.buffer.base
	s.buffer.mu.Unlock()
	if generation != 1 || segments != 1 || base != 0 {
		t.Fatalf("rejected request mutated session: gen=%d segments=%d base=%d", generation, segments, base)
	}
	if resp := request("probe", "256", 256, 1); resp.StatusCode != 200 || resp.Header.Get(rxSegmentEndHeader) != "" {
		t.Fatal("budget blocked final control probe")
	}
}

func TestRXServerResumeOwnershipAndFinalOffset(t *testing.T) {
	var accepts atomic.Int32
	h, srv := rxServerFixture(t, func(c stat.Connection) { accepts.Add(1); go func() { c.Write([]byte("0123456789")); c.Close() }() })
	token := strings.Repeat("ab", 32)
	r := rxTestRequest(t, srv, "one", "open", token, 0, 1)
	if r.StatusCode != 200 || r.Header.Get(rxOffsetHeader) != "0" || r.Header.Get(rxNonceHeader) != "0011223344556677" {
		t.Fatal("bad negotiation", r.Status, r.Header)
	}
	p := make([]byte, 4)
	if _, err := io.ReadFull(r.Body, p); err != nil || string(p) != "0123" {
		t.Fatal(err, string(p))
	}
	r.Body.Close()
	if r := rxTestRequest(t, srv, "one", "probe", strings.Repeat("cd", 32), 4, 1); r.StatusCode != 403 {
		t.Fatal("wrong token", r.Status)
	}
	if r := rxTestRequest(t, srv, "one", "resume", token, 11, 2); r.StatusCode != 410 {
		t.Fatal("future offset", r.Status)
	}
	if r := rxTestRequest(t, srv, "two", "open", token, 0, 1); r.StatusCode != 429 {
		t.Fatal("capacity", r.Status)
	}
	r = rxTestRequest(t, srv, "one", "resume", token, 4, 2)
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != "456789" {
		t.Fatal("resume data", string(got), err)
	}
	if accepts.Load() != 1 {
		t.Fatal("reopened upper session", accepts.Load())
	}
	r = rxTestRequest(t, srv, "one", "probe", token, 10, 1)
	if r.StatusCode != 200 || r.Header.Get(rxEndHeader) != "10" || r.Header.Get(rxDoneHeader) != "1" {
		t.Fatal("final proof", r.Status, r.Header)
	}
	if r := rxTestRequest(t, srv, "one", "resume", token, 4, 3); r.StatusCode != 410 {
		t.Fatal("discarded data replay", r.Status)
	}
	if r := rxTestRequest(t, srv, "one", "close", token, 10, 2); r.StatusCode != 200 {
		t.Fatal("close", r.Status)
	}
	h.sessionMu.Lock()
	count := len(h.rxSessions)
	h.sessionMu.Unlock()
	if count != 0 {
		t.Fatal("session retained after close")
	}
	if r := rxTestRequest(t, srv, "one", "resume", token, 10, 3); r.StatusCode != 410 {
		t.Fatal("recreated deleted session", r.Status)
	}
}

func TestRXServerIdleNotClosedByWriteDeadline(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(strconv.FormatBool(h2), func(t *testing.T) {
			h, srv := rxServerFixture(t, func(c stat.Connection) {
				go func() { time.Sleep(100 * time.Millisecond); c.Write([]byte("awake")); c.Close() }()
			}, h2)
			h.config.Rx.RequestTimeoutMs = 30
			r := rxTestRequest(t, srv, "idle", "open", strings.Repeat("12", 32), 0, 1)
			p, err := io.ReadAll(r.Body)
			if err != nil || string(p) != "awake" {
				t.Fatal("idle stream expired", string(p), err)
			}
		})
	}
}

func TestRXServerGenerationReplacesOnlyDownlink(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(strconv.FormatBool(h2), func(t *testing.T) {
			var accepts atomic.Int32
			h, srv := rxServerFixture(t, func(c stat.Connection) { accepts.Add(1); go func() { c.Write([]byte("headtail")) }() }, h2)
			token := strings.Repeat("56", 32)
			first := rxTestRequest(t, srv, "live", "open", token, 0, 1)
			var head [4]byte
			if _, err := io.ReadFull(first.Body, head[:]); err != nil {
				t.Fatal(err)
			}
			second := rxTestRequest(t, srv, "live", "resume", token, 4, 2)
			if second.StatusCode != 200 {
				t.Fatal(second.Status)
			}
			if _, err := io.ReadFull(second.Body, head[:]); err != nil || string(head[:]) != "tail" {
				t.Fatal(string(head[:]), err)
			}
			if r := rxTestRequest(t, srv, "live", "close", token, 8, 1); r.StatusCode != 409 {
				t.Fatal("stale close", r.Status)
			}
			h.sessionMu.Lock()
			before := h.rxSessions["live"].lastSeen
			h.sessionMu.Unlock()
			if r := rxTestRequest(t, srv, "live", "probe", token, 8, 1); r.StatusCode != 200 {
				t.Fatal("stale ACK", r.Status)
			}
			h.sessionMu.Lock()
			after := h.rxSessions["live"].lastSeen
			h.sessionMu.Unlock()
			if before != after {
				t.Fatal("stale probe refreshed expiry")
			}
			if accepts.Load() != 1 {
				t.Fatal("logical connection recreated")
			}
			if r := rxTestRequest(t, srv, "live", "close", token, 8, 2); r.StatusCode != 200 {
				t.Fatal(r.Status)
			}
			if _, err := io.ReadAll(second.Body); err != nil && h2 == false {
				t.Log("closed downstream", err)
			}
		})
	}
}

func TestRXServerExpiryReleasesBlockedWriter(t *testing.T) {
	written := make(chan error, 1)
	h, srv := rxServerFixture(t, func(c stat.Connection) { go func() { _, err := c.Write(make([]byte, 512*1024)); written <- err }() })
	r := rxTestRequest(t, srv, "expire", "open", strings.Repeat("34", 32), 0, 1)
	r.Body.Close()
	h.sessionMu.Lock()
	s := h.rxSessions["expire"]
	s.lastSeen = time.Now().Add(-time.Minute)
	h.sessionMu.Unlock()
	h.expireRX("expire", s, time.Second)
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("writer must fail on destroyed session")
		}
	case <-time.After(time.Second):
		t.Fatal("writer leaked")
	}
	if _, err := s.buffer.read(context.Background(), 0); err == nil {
		t.Fatal("expired buffer readable")
	}
}
