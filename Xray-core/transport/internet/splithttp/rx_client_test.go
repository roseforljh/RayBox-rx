package splithttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func rxTestClientOptions() *RXConfig {
	return &RXConfig{
		Enabled: true, StallDetect: true, MaxReplayBytes: 4096, MaxSessions: 4,
		ProbeIntervalMs: 10, StallTimeoutMs: 40, RequestTimeoutMs: 200,
		SessionTtlSecs: 5, MaxReconnects: 4, RetryDelayMs: 1,
	}
}

func rxTestConfirm(w http.ResponseWriter, req *http.Request, end uint64, done bool) {
	w.Header().Set(rxVersionHeader, "1")
	w.Header().Set(rxOffsetHeader, req.Header.Get(rxOffsetHeader))
	w.Header().Set(rxNonceHeader, req.Header.Get(rxNonceHeader))
	w.Header().Set(rxWindowHeader, "4096")
	w.Header().Set(rxEndHeader, strconv.FormatUint(end, 10))
	w.Header().Set(rxDoneHeader, "0")
	if done {
		w.Header().Set(rxDoneHeader, "1")
	}
}

func rxTestNewClient(t *testing.T, ctx context.Context, server *httptest.Server, options *RXConfig) *rxDownlink {
	t.Helper()
	factory := func() *http.Client {
		return &http.Client{Transport: server.Client().Transport.(*http.Transport).Clone()}
	}
	r, err := newRXDownlink(ctx, &Config{Mode: "packet-up", Path: "/"}, options, server.URL+"/", "test-session", factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestRXClientSegments(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size, limit int
		shortFirst  bool
		wantError   bool
	}{
		{"short-final", 777, 4, false, false},
		{"exact-final-budget", 512, 2, false, false},
		{"budget-incomplete", 777, 2, false, true},
		{"early-eof", 777, 8, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := strings.Repeat("x", tc.size)
			var gets atomic.Uint64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				offset, _ := strconv.ParseUint(req.Header.Get(rxOffsetHeader), 10, 64)
				rxTestConfirm(w, req, uint64(tc.size), req.Method == "HEAD")
				w.Header().Set(rxSegmentHeader, "256")
				if req.Method == "HEAD" {
					return
				}
				gets.Add(1)
				w.Header().Set(rxSegmentEndHeader, strconv.FormatUint(offset+256, 10))
				end := min(int(offset)+256, tc.size)
				if tc.shortFirst && offset == 0 {
					end = 73
				}
				io.WriteString(w, payload[int(offset):end])
			}))
			defer srv.Close()
			opts := rxTestClientOptions()
			opts.StallDetect, opts.MaxResponseBytes, opts.MaxSegments = false, 256, int32(tc.limit)
			opts.ProbeIntervalMs = 1000
			r := rxTestNewClient(t, context.Background(), srv, opts)
			got, err := io.ReadAll(r)
			if (err != nil) != tc.wantError {
				t.Fatalf("bytes=%d error=%v", len(got), err)
			}
			if !tc.wantError && string(got) != payload {
				t.Fatal("segmented stream corrupted")
			}
			s := r.statsSnapshot()
			if gets.Load() > uint64(tc.limit) || s.SegmentRequests != gets.Load() {
				t.Fatalf("unbounded requests: gets=%d stats=%+v", gets.Load(), s)
			}
			if tc.shortFirst != (s.ReconnectAttempts != 0) || (!tc.shortFirst && s.Recoveries != 0) {
				t.Fatalf("wrong fault accounting: %+v", s)
			}
		})
	}
}

func TestRXClientSegmentConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(http.Header)
		wantError bool
	}{
		{"old-rx", func(h http.Header) { h.Del(rxSegmentHeader); h.Del(rxSegmentEndHeader) }, false},
		{"only-budget", func(h http.Header) { h.Del(rxSegmentEndHeader) }, true},
		{"only-boundary", func(h http.Header) { h.Del(rxSegmentHeader) }, true},
		{"zero", func(h http.Header) { h.Set(rxSegmentHeader, "0") }, true},
		{"oversized", func(h http.Header) { h.Set(rxSegmentHeader, "1025") }, true},
		{"wrong-boundary", func(h http.Header) { h.Set(rxSegmentEndHeader, "1023") }, true},
		{"duplicate", func(h http.Header) { h.Add(rxSegmentHeader, "1024") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				rxTestConfirm(w, req, 0, false)
				w.Header().Set(rxSegmentHeader, "1024")
				if req.Method == "GET" {
					w.Header().Set(rxSegmentEndHeader, "1024")
				}
				tc.mutate(w.Header())
			}))
			defer srv.Close()
			opts := rxTestClientOptions()
			opts.MaxResponseBytes, opts.MaxSegments = 1024, 8
			r, err := newRXDownlink(context.Background(), &Config{}, opts, srv.URL, "sid", func() *http.Client {
				return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if r != nil {
				if r.segment != 0 {
					t.Fatal("old peer unexpectedly enabled segments")
				}
				r.Close()
			}
		})
	}
}

func TestRXSegmentContextAndStickyConfirmation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			type contextKey struct{}
			key := contextKey{}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key, "instance"))
			defer cancel()
			var missing atomic.Bool
			var seen sync.Map
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				action := req.Header.Get(rxActionHeader)
				seen.Store(action, true)
				rxTestConfirm(w, req, 512, false)
				if action == "upload" {
					return
				}
				w.Header().Set(rxSegmentHeader, "256")
				if req.Method == "GET" {
					offset, _ := strconv.ParseUint(req.Header.Get(rxOffsetHeader), 10, 64)
					w.Header().Set(rxSegmentEndHeader, strconv.FormatUint(offset+256, 10))
					if changed && action == "resume" {
						w.Header().Del(rxSegmentHeader)
					}
					io.WriteString(w, strings.Repeat("x", 256))
				}
			}))
			defer srv.Close()
			factory := func() *http.Client {
				return &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if ctx.Value(key) != "instance" {
						missing.Store(true)
					}
					return (&net.Dialer{}).DialContext(ctx, network, address)
				}}}
			}
			opts := rxTestClientOptions()
			opts.MaxResponseBytes, opts.MaxSegments, opts.ProbeIntervalMs = 256, 8, 1000
			r, err := newRXDownlink(ctx, &Config{}, opts, srv.URL, "sid", factory)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if _, _, err := r.probe(); err != nil {
				t.Fatal(err)
			}
			template, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
			if err := r.postPacket(template, "0"); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(r, make([]byte, 256)); err != nil {
				t.Fatal(err)
			}
			_, err = r.Read(make([]byte, 1))
			if changed != (err != nil) {
				t.Fatalf("sticky capability error=%v", err)
			}
			cancel()
			r.Close()
			if missing.Load() {
				t.Fatal("protected instance context lost")
			}
			for _, action := range []string{"open", "resume", "probe", "upload", "close"} {
				if _, ok := seen.Load(action); !ok {
					t.Fatalf("missing action %s", action)
				}
			}
		})
	}
}

func TestRXClientNegotiation(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(http.Header)
		wantError bool
	}{
		{"valid", func(http.Header) {}, false},
		{"missing-offset", func(h http.Header) { h.Del(rxOffsetHeader) }, true},
		{"wrong-offset", func(h http.Header) { h.Set(rxOffsetHeader, "1") }, true},
		{"wrong-nonce", func(h http.Header) { h.Set(rxNonceHeader, "cached-response") }, true},
		{"unknown-version", func(h http.Header) { h.Set(rxVersionHeader, "2") }, true},
		{"missing-window", func(h http.Header) { h.Del(rxWindowHeader) }, true},
		{"body-transformed", func(h http.Header) { h.Set("Content-Encoding", "gzip") }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				rxTestConfirm(w, req, 0, true)
				test.mutate(w.Header())
			}))
			defer s.Close()
			r, err := newRXDownlink(context.Background(), &Config{}, rxTestClientOptions(), s.URL, "sid", func() *http.Client { return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()} })
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
			if r != nil {
				r.Close()
			}
		})
	}
}

type rxTailReader struct{}

func (rxTailReader) Read(p []byte) (int, error) {
	return copy(p, "tail"), io.ErrUnexpectedEOF
}
func (rxTailReader) Close() error { return nil }

func TestRXClientCancelledBodyTailIsNotRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &rxDownlink{
		ctx: ctx, cancel: cancel, opts: rxTestClientOptions(),
		current: &rxHTTPBody{ReadCloser: rxTailReader{}},
		window:  4096, ackWake: make(chan struct{}, 1),
		recoveryStart: time.Now(), // monitor has cancelled the old body
	}
	p := make([]byte, 4)
	n, err := r.Read(p)
	if n != 4 || err != nil || string(p) != "tail" {
		t.Fatalf("tail n=%d err=%v data=%q", n, err, p)
	}
	if r.offset != 4 || r.pending != io.ErrUnexpectedEOF || r.stats.Recoveries != 0 || r.recoveryStart.IsZero() {
		t.Fatalf("tail discarded or counted as recovery: offset=%d pending=%v stats=%+v", r.offset, r.pending, r.stats)
	}
}

func TestRXClientLegacyFallback(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		io.WriteString(w, "ordinary byte stream")
	}))
	defer s.Close()
	r := rxTestNewClient(t, context.Background(), s, rxTestClientOptions())
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "ordinary byte stream" || !r.legacy {
		t.Fatalf("%q err=%v legacy=%v", b, err, r.legacy)
	}
	r.Close()
	if requests.Load() != 1 {
		t.Fatalf("legacy endpoint received %d requests", requests.Load())
	}
}

func TestRXClientResumeRejectsMissingConfirmation(t *testing.T) {
	var resumes atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Header.Get(rxActionHeader) {
		case "open":
			rxTestConfirm(w, req, 6, false)
			io.WriteString(w, "abc")
		case "resume":
			resumes.Add(1)
			io.WriteString(w, "abcdef") // old/stripped response must never reach caller
		default:
			rxTestConfirm(w, req, 6, true)
		}
	}))
	defer s.Close()
	r := rxTestNewClient(t, context.Background(), s, rxTestClientOptions())
	b, err := io.ReadAll(r)
	if err == nil || string(b) != "abc" || resumes.Load() != 1 {
		t.Fatalf("%q err=%v resumes=%d", b, err, resumes.Load())
	}
}

func TestRXClientResumeConfirmsEOFWithoutProbe(t *testing.T) {
	var probes, resumes atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		action := req.Header.Get(rxActionHeader)
		rxTestConfirm(w, req, 3, action != "open")
		switch action {
		case "open":
			io.WriteString(w, "end")
		case "resume":
			resumes.Add(1)
			if req.Header.Get(rxOffsetHeader) != "3" {
				t.Errorf("resume offset=%q", req.Header.Get(rxOffsetHeader))
			}
		case "probe":
			probes.Add(1)
		}
	}))
	defer s.Close()
	opts := rxTestClientOptions()
	opts.ProbeIntervalMs = 10000
	r := rxTestNewClient(t, context.Background(), s, opts)
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "end" || resumes.Load() != 1 || probes.Load() != 0 {
		t.Fatalf("data=%q err=%v resumes=%d probes=%d", b, err, resumes.Load(), probes.Load())
	}
	r.mu.Lock()
	ack := r.ackOffset
	r.mu.Unlock()
	if ack != 3 {
		t.Fatalf("resume failed to record ACK: %d", ack)
	}
	// Re-reading a proven terminal stream must not start another resume.
	if n, err := r.Read(make([]byte, 1)); n != 0 || err != io.EOF || resumes.Load() != 1 {
		t.Fatalf("terminal n=%d err=%v resumes=%d", n, err, resumes.Load())
	}
}

func TestRXClientCoalescesAcknowledgedWakeups(t *testing.T) {
	var probes atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		probes.Add(1)
		rxTestConfirm(w, req, 2048, false)
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := rxTestClientOptions()
	opts.ProbeIntervalMs = 10000
	r := &rxDownlink{
		ctx: ctx, cancel: cancel, config: &Config{}, opts: opts,
		url: s.URL, sessionID: "sid", token: strings.Repeat("a", 64), generation: 1,
		offset: 1024, ackOffset: 1024, window: 4096, ackWake: make(chan struct{}, 1),
		factory: func() *http.Client { return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()} },
	}
	done := make(chan struct{})
	go func() { defer close(done); r.monitor() }()
	defer func() { cancel(); <-done }()
	r.ackWake <- struct{}{}
	time.Sleep(30 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatal("already acknowledged wakeup created a TCP request")
	}
	r.mu.Lock()
	r.offset = 2048
	r.mu.Unlock()
	r.ackWake <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		ack := r.ackOffset
		r.mu.Unlock()
		if ack == 2048 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new read progress was not acknowledged")
		}
		time.Sleep(time.Millisecond)
	}
	r.ackWake <- struct{}{}
	time.Sleep(30 * time.Millisecond)
	if probes.Load() != 1 {
		t.Fatalf("expected one ACK for new progress, got %d", probes.Load())
	}
}

func TestRXClientHTTP2ResumeUsesNewTCP(t *testing.T) {
	var mu sync.Mutex
	var peers []string
	var wrongProtocol atomic.Bool
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.ProtoMajor != 2 {
			wrongProtocol.Store(true)
		}
		action := req.Header.Get(rxActionHeader)
		rxTestConfirm(w, req, 8, true)
		if req.Method == http.MethodGet {
			mu.Lock()
			peers = append(peers, req.RemoteAddr)
			mu.Unlock()
			if action == "open" {
				io.WriteString(w, "abcd")
			} else {
				if req.Header.Get(rxOffsetHeader) != "4" {
					t.Errorf("resume offset=%q", req.Header.Get(rxOffsetHeader))
				}
				io.WriteString(w, "efgh")
			}
		}
	}))
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	r := rxTestNewClient(t, context.Background(), s, rxTestClientOptions())
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "abcdefgh" {
		t.Fatalf("%q err=%v", b, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if wrongProtocol.Load() || len(peers) != 2 || peers[0] == peers[1] {
		t.Fatalf("H2=%v TCP peers=%v", !wrongProtocol.Load(), peers)
	}
	stats := r.statsSnapshot()
	if stats.Recoveries != 1 || stats.ReconnectAttempts != 1 || len(stats.RecoveryDurations) != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestRXClientReconnectBudget(t *testing.T) {
	var gets atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rxTestConfirm(w, req, 1, false)
		if req.Method == http.MethodGet {
			gets.Add(1)
		}
	}))
	defer s.Close()
	opts := rxTestClientOptions()
	opts.MaxReconnects = 2
	r := rxTestNewClient(t, context.Background(), s, opts)
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "budget exhausted") || gets.Load() != 3 {
		t.Fatalf("err=%v gets=%d", err, gets.Load())
	}
}

func TestRXClientConfirmedStall(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rxTestConfirm(w, req, 8, true)
		switch req.Header.Get(rxActionHeader) {
		case "open":
			io.WriteString(w, "abcd")
			w.(http.Flusher).Flush()
			<-req.Context().Done()
		case "resume":
			if req.Header.Get(rxOffsetHeader) != "4" {
				t.Errorf("offset=%s", req.Header.Get(rxOffsetHeader))
			}
			io.WriteString(w, "efgh")
		}
	}))
	defer s.Close()
	r := rxTestNewClient(t, context.Background(), s, rxTestClientOptions())
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "abcdefgh" {
		t.Fatalf("%q err=%v", b, err)
	}
	stats := r.statsSnapshot()
	if stats.ConfirmedStalls != 1 || stats.Recoveries != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestRXClientIdleAndHeaderDeadline(t *testing.T) {
	release := make(chan struct{})
	var produced atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(rxActionHeader) == "open" {
			rxTestConfirm(w, req, 0, false)
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-release:
				produced.Store(true)
				io.WriteString(w, "end")
			case <-req.Context().Done():
			}
		} else {
			var end uint64
			if produced.Load() {
				end = 3
			}
			rxTestConfirm(w, req, end, produced.Load())
		}
	}))
	defer s.Close()
	opts := rxTestClientOptions()
	opts.RequestTimeoutMs = 40
	r := rxTestNewClient(t, context.Background(), s, opts)
	type result struct {
		b   []byte
		err error
	}
	finished := make(chan result, 1)
	go func() { b, err := io.ReadAll(r); finished <- result{b, err} }()
	time.Sleep(180 * time.Millisecond) // exceeds header deadline and multiple stall intervals
	if stats := r.statsSnapshot(); stats.ReconnectAttempts != 0 || stats.ConfirmedStalls != 0 {
		t.Fatalf("normal idle misclassified: %+v", stats)
	}
	close(release)
	select {
	case res := <-finished:
		if res.err != nil || string(res.b) != "end" {
			t.Fatalf("%q err=%v", res.b, res.err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not finish")
	}
}

func TestRXClientCancellationUnblocksHeadersAndBody(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(strconv.FormatBool(headers), func(t *testing.T) {
			entered := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get(rxActionHeader) != "open" {
					rxTestConfirm(w, req, 0, false)
					return
				}
				if headers {
					rxTestConfirm(w, req, 0, false)
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-req.Context().Done()
			}))
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				r, err := newRXDownlink(ctx, &Config{}, rxTestClientOptions(), s.URL, "sid", func() *http.Client { return &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()} })
				if err == nil {
					defer r.Close()
					_, err = r.Read(make([]byte, 1))
				}
				finished <- err
			}()
			<-entered
			cancel()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("cancellation returned no error")
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not unblock request")
			}
		})
	}
}

func TestRXHedgeDial(t *testing.T) {
	// The first handshake stalls until cancelled; the hedge after rxHedgeDelay wins
	// and the stalled attempt is cancelled rather than left open.
	var calls atomic.Int32
	stalledCancelled := make(chan struct{})
	dial := rxHedgeDial(func(ctx context.Context) (net.Conn, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			close(stalledCancelled)
			return nil, ctx.Err()
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	})
	started := time.Now()
	conn, err := dial(context.Background())
	if err != nil || conn == nil {
		t.Fatalf("hedged dial failed: %v", err)
	}
	conn.Close()
	if elapsed := time.Since(started); elapsed < rxHedgeDelay || elapsed > rxHedgeDelay+time.Second {
		t.Fatalf("hedge started after %v", elapsed)
	}
	select {
	case <-stalledCancelled:
	case <-time.After(time.Second):
		t.Fatal("stalled attempt was not cancelled")
	}

	// Fast failures retry immediately and stop at the attempt budget.
	calls.Store(0)
	failing := rxHedgeDial(func(context.Context) (net.Conn, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})
	if _, err := failing(context.Background()); err != io.ErrUnexpectedEOF || calls.Load() != rxHedgeAttempts {
		t.Fatalf("got err %v after %d attempts", err, calls.Load())
	}
}

func TestRXSpreadAddressesNormalize(t *testing.T) {
	base := func(addresses []string) *Config {
		return &Config{Mode: "packet-up", Rx: &RXConfig{Enabled: true, MaxResponseBytes: 1024, SpreadAddresses: addresses}}
	}
	if n, err := base([]string{"104.21.44.55", "2606:4700::1"}).NormalizeRX(); err != nil || len(n.SpreadAddresses) != 2 {
		t.Fatalf("valid literals rejected: %v", err)
	}
	for _, bad := range [][]string{{"example.com"}, make([]string, 65)} {
		if _, err := base(bad).NormalizeRX(); err == nil {
			t.Fatalf("accepted %v", bad[:1])
		}
	}
	c := base([]string{"104.21.44.55"})
	c.Rx.MaxResponseBytes = 0
	if _, err := c.NormalizeRX(); err == nil {
		t.Fatal("spread without segments accepted")
	}
}

func TestRXSpreadPrefersHealthyAddress(t *testing.T) {
	s := newRXSpread([]string{"192.0.2.1", "192.0.2.2", "192.0.2.3"})
	for range 50 {
		s.report(0, false)
		s.report(1, false)
	}
	picks := [3]int{}
	for range 3000 {
		picks[s.pick()]++
	}
	// A dead address is only chosen when both samples hit dead ones (about 4/9),
	// so the healthy one must dominate but the others are still probed.
	if picks[2] < 1500 || picks[0] == 0 || picks[1] == 0 {
		t.Fatalf("picks %v", picks)
	}
	s.report(2, true)
	if s.scores[2] != 1 {
		t.Fatalf("score %v", s.scores[2])
	}
}
