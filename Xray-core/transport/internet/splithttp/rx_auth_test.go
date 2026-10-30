package splithttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/stat"
)

func rxAuthTestConfig() *Config {
	return &Config{Mode: "packet-up", Path: "/secure/", XPaddingBytes: &RangeConfig{From: 100, To: 100},
		Rx: &RXConfig{Enabled: true, AuthKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x73}, 32)),
			MaxReplayBytes: 262144, MaxSessions: 2, MaxResponseBytes: 4096, MaxConcurrentSegments: 16,
			SessionTtlSecs: 10, RequestTimeoutMs: 1000, ProbeIntervalMs: 100, MaxSegments: 4096}}
}

func rxAuthTestHandler(c *Config) *requestHandler {
	return &requestHandler{config: c, path: c.GetNormalizedPath(), sessionMu: &sync.Mutex{}}
}

func rxAuthTestClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true // production RX owns one TCP per request
	return &http.Client{Transport: transport}
}

func rxAuthTestRequest(t testing.TB, codec *rxAuthCodec, action string) (*http.Request, uint64) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://example.test/secure/session", nil)
	if action == "close" || action == "probe" {
		req.Method = "HEAD"
	}
	req.Header.Set(rxVersionHeader, "1")
	req.Header.Set(rxTokenHeader, strings.Repeat("ab", 32))
	req.Header.Set(rxNonceHeader, "0011223344556677")
	req.Header.Set(rxActionHeader, action)
	req.Header.Set(rxOffsetHeader, "0")
	req.Header.Set(rxGenerationHeader, "1")
	counter, err := codec.sealRequest(req, "session", "")
	if err != nil {
		t.Fatal(err)
	}
	return req, counter
}

func TestRXAuthRequestForgeryAndReplay(t *testing.T) {
	c := rxAuthTestConfig()
	codec, err := newRXClientAuth(c.Rx.AuthKey, "session")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := rxAuthTestRequest(t, codec, "open")
	if req.Header.Get(rxTokenHeader) != "" || req.Header.Get(rxNonceHeader) != "" || req.Header.Get(rxActionHeader) != "" {
		t.Fatal("control secrets escaped envelope")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing", func(r *http.Request) { r.Header.Del(rxControlHeader) }},
		{"legacy", func(r *http.Request) { r.Header.Set(rxVersionHeader, "1") }},
		{"clear-token", func(r *http.Request) { r.Header.Set(rxTokenHeader, strings.Repeat("ab", 32)) }},
		{"clear-close", func(r *http.Request) { r.Header.Set(rxActionHeader, "close") }},
		{"duplicate", func(r *http.Request) { r.Header.Add(rxControlHeader, r.Header.Get(rxControlHeader)) }},
		{"method", func(r *http.Request) { r.Method = "HEAD" }},
		{"path", func(r *http.Request) { r.URL.Path = "/other/session" }},
		{"ciphertext", func(r *http.Request) {
			b, _ := base64.RawURLEncoding.DecodeString(r.Header.Get(rxControlHeader))
			b[len(b)-1] ^= 1
			r.Header.Set(rxControlHeader, base64.RawURLEncoding.EncodeToString(b))
		}},
		{"oversize", func(r *http.Request) { r.Header.Set(rxControlHeader, strings.Repeat("a", rxAuthMaxEnvelope+1)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := rxAuthTestHandler(c)
			altered := req.Clone(req.Context())
			tc.mutate(altered)
			w := httptest.NewRecorder()
			_, _, ok := h.authenticateRX(w, altered, "session", "")
			if ok || w.Code != 403 {
				t.Fatal("forged request accepted")
			}
			if h.rxAuth != nil && len(h.rxAuth.seen) != 0 {
				t.Fatal("forgery allocated replay state")
			}
		})
	}
	h := rxAuthTestHandler(c)
	_, decoded, ok := h.authenticateRX(httptest.NewRecorder(), req, "session", "")
	if !ok || !validRXHex(decoded.Header.Get(rxTokenHeader), 32) || decoded.Header.Get(rxTokenHeader) == strings.Repeat("ab", 32) {
		t.Fatal("valid control failed")
	}
	if _, _, ok := h.authenticateRX(httptest.NewRecorder(), req, "session", ""); ok {
		t.Fatal("captured request replay accepted")
	}
	// RX shutdown does not discard the replay tombstones during their validity.
	h.closeRX()
	if _, _, ok := h.authenticateRX(httptest.NewRecorder(), req, "session", ""); ok {
		t.Fatal("replay accepted after shutdown")
	}
}

func TestRXAuthRestartRotatesSessionCapability(t *testing.T) {
	c := rxAuthTestConfig()
	codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
	open, _ := rxAuthTestRequest(t, codec, "open")
	first := rxAuthTestHandler(c)
	_, previous, ok := first.authenticateRX(httptest.NewRecorder(), open, "session", "")
	if !ok {
		t.Fatal("open refused")
	}
	oldToken := previous.Header.Get(rxTokenHeader)
	closeReq := httptest.NewRequest("HEAD", open.URL.String(), nil)
	closeReq.Header = previous.Header.Clone()
	closeReq.Header.Set(rxActionHeader, "close")
	if _, err := codec.sealRequest(closeReq, "session", ""); err != nil {
		t.Fatal(err)
	}
	restarted := rxAuthTestHandler(c)
	_, current, ok := restarted.authenticateRX(httptest.NewRecorder(), open, "session", "")
	if !ok {
		t.Fatal("open after restart refused")
	}
	newToken := current.Header.Get(rxTokenHeader)
	if oldToken == newToken {
		t.Fatal("capability reused across restart")
	}
	restarted.rxSessions = map[string]*rxSession{"session": {token: newToken}}
	recorder := httptest.NewRecorder()
	w, decoded, ok := restarted.authenticateRX(recorder, closeReq, "session", "")
	if !ok {
		t.Fatal("fixture should reach session capability check")
	}
	_, handled := restarted.handleRX(w, decoded, "session", "", nil)
	if !handled || recorder.Code != 403 || restarted.rxSessions["session"] == nil {
		t.Fatal("captured old close affected restarted session")
	}
}

func TestRXAuthReorderWindowAndClock(t *testing.T) {
	var replay rxAuthReplay
	for _, n := range []uint64{512, 1, 511, 513, 255, 1024, 1023} {
		if !replay.accept(n) {
			t.Fatalf("valid reordered counter %d", n)
		}
	}
	for _, n := range []uint64{0, 512, 1, 511, 513, 255, 1024, 1023, 1 << 32} {
		if replay.accept(n) {
			t.Fatalf("duplicate/invalid counter %d", n)
		}
	}
	if !replay.accept(2000) || replay.accept(900) {
		t.Fatal("old counters survived window")
	}
	// Compare bitmap behavior to a simple reference over deterministic shifts.
	var reference rxAuthReplay
	seen := map[uint64]bool{}
	high := uint64(0)
	for i := uint64(1); i < 8000; i++ {
		n := i*37%7000 + 1
		want := !seen[n] && (n > high || high-n < rxAuthWindow)
		if got := reference.accept(n); got != want {
			t.Fatalf("counter %d: %v want %v", n, got, want)
		}
		if want {
			seen[n] = true
			high = max(high, n)
		}
	}
	c := rxAuthTestConfig()
	codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
	for _, delta := range []time.Duration{-rxAuthSkew - time.Minute, rxAuthSkew + time.Minute} {
		req, _ := rxAuthTestRequest(t, codec, "open")
		// Decrypt a test request and reseal with a correctly authenticated stale clock.
		wire, _ := base64.RawURLEncoding.DecodeString(req.Header.Get(rxControlHeader))
		counter := binary.BigEndian.Uint64(wire[16:24])
		binding := rxAuthBinding(req.Method, req.URL.EscapedPath(), "session", "", 0)
		data, _ := codec.request.Open(nil, rxAuthNonce(counter), wire[24:], binding)
		binary.BigEndian.PutUint64(data[:8], uint64(time.Now().Add(delta).Unix()))
		wire = codec.request.Seal(wire[:24], rxAuthNonce(counter), data, binding)
		req.Header.Set(rxControlHeader, base64.RawURLEncoding.EncodeToString(wire))
		if _, _, ok := rxAuthTestHandler(c).authenticateRX(httptest.NewRecorder(), req, "session", ""); ok {
			t.Fatal("stale/future request accepted")
		}
	}
}

func TestRXAuthRestartResponsesUseFreshNonce(t *testing.T) {
	c := rxAuthTestConfig()
	codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
	req, counter := rxAuthTestRequest(t, codec, "open")
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		// Simulate replay-cache loss at a server restart. The request/key/
		// counter are identical, but the server capability and response vary.
		recorder := httptest.NewRecorder()
		w, _, ok := rxAuthTestHandler(c).authenticateRX(recorder, req, "session", "")
		if !ok {
			t.Fatal("restart fixture refused request")
		}
		w.WriteHeader(http.StatusOK)
		resp := recorder.Result()
		wire, err := base64.RawURLEncoding.Strict().DecodeString(resp.Header.Get(rxControlHeader))
		if err != nil || len(wire) < 28 {
			t.Fatal("invalid encrypted response")
		}
		// Go's random-nonce GCM wire format starts with its 96-bit nonce.
		nonce := string(wire[:12])
		if seen[nonce] {
			t.Fatal("response reused an AES-GCM nonce across server restarts")
		}
		seen[nonce] = true
		if err := codec.openResponse(resp, req.Method, req.URL.EscapedPath(), "session", "", counter); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRXAuthDisabledPreservesOrdinaryUpload(t *testing.T) {
	c := rxAuthTestConfig()
	c.Rx.Enabled = false // Preserve an existing key when RX is switched off.
	c.UplinkDataPlacement = PlacementHeader
	c.UplinkDataKey = "data"
	h := rxAuthTestHandler(c)
	h.ln = &Listener{config: c}
	session := h.upsertSession("ordinary")
	defer session.uploadQueue.Close()
	payload := []byte("ordinary-upload")
	req := httptest.NewRequest("POST", "http://example.test/secure/", nil)
	c.FillPacketRequest(req, "ordinary", "0", buf.MergeBytes(nil, payload))
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("RX disabled rejected ordinary upload: %d", recorder.Code)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(session.uploadQueue, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("ordinary payload %q: %v", got, err)
	}
}

func TestRXAuthResponseBinding(t *testing.T) {
	c := rxAuthTestConfig()
	codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
	req, counter := rxAuthTestRequest(t, codec, "open")
	recorder := httptest.NewRecorder()
	w, _, ok := rxAuthTestHandler(c).authenticateRX(recorder, req, "session", "")
	if !ok {
		t.Fatal("valid request rejected")
	}
	w.Header().Set(rxVersionHeader, "1")
	w.Header().Set(rxNonceHeader, "0011223344556677")
	w.Header().Set(rxEndHeader, "1234")
	w.WriteHeader(200)
	if recorder.Header().Get(rxNonceHeader) != "" || recorder.Header().Get(rxEndHeader) != "" {
		t.Fatal("response metadata visible")
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*http.Response)
		counter uint64
		id      string
	}{
		{"valid", func(*http.Response) {}, counter, "session"},
		{"other-request", func(*http.Response) {}, counter + 1, "session"},
		{"other-session", func(*http.Response) {}, counter, "other"},
		{"status", func(r *http.Response) { r.StatusCode = 201 }, counter, "session"},
		{"downgrade", func(r *http.Response) { r.Header = make(http.Header) }, counter, "session"},
		{"reflection", func(r *http.Response) { r.Header = req.Header.Clone() }, counter, "session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recorder.Result()
			r.Header = r.Header.Clone()
			tc.mutate(r)
			err := codec.openResponse(r, req.Method, req.URL.EscapedPath(), tc.id, "", tc.counter)
			if (err == nil) != (tc.name == "valid") {
				t.Fatalf("response auth: %v", err)
			}
		})
	}
}

func TestRXAuthClientRequiresSecureConfirmation(t *testing.T) {
	c := rxAuthTestConfig()
	opts, err := c.NormalizeRX()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"stripped", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "legacy" {
					w.Header().Set(rxVersionHeader, "1")
				}
				w.WriteHeader(200)
			}))
			defer srv.Close()
			r, err := newRXDownlink(context.Background(), c, opts, srv.URL+"/secure/", "session", func() *http.Client { return srv.Client() })
			if r != nil {
				r.Close()
			}
			if err == nil {
				t.Fatal("authenticated client silently fell back")
			}
		})
	}
}

func TestRXAuthUploadAndParallelStream(t *testing.T) {
	for _, placement := range []string{PlacementBody, PlacementHeader, PlacementCookie} {
		t.Run(placement, func(t *testing.T) {
			c := rxAuthTestConfig()
			c.UplinkDataPlacement = placement
			c.UplinkDataKey = "data"
			c.ScMaxEachPostBytes = &RangeConfig{From: 4096, To: 4096}
			opts, err := c.NormalizeRX()
			if err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("ordered-authenticated-stream"), 4096)
			h := rxAuthTestHandler(c)
			h.ln = &Listener{config: c, addConn: func(conn stat.Connection) {
				go func() {
					defer conn.Close()
					data := make([]byte, len(payload))
					if _, err := io.ReadFull(conn, data); err == nil {
						conn.Write(data)
					}
				}()
			}}
			srv := httptest.NewServer(h)
			defer srv.Close()
			defer h.closeRX()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			r, err := newRXDownlink(ctx, c, opts, srv.URL+c.GetNormalizedPath(), "session", rxAuthTestClient)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			client := &DefaultDialerClient{transportConfig: c}
			for offset, seq := 0, 0; offset < len(payload); seq++ {
				end := min(len(payload), offset+4096)
				if err := client.PostPacket(context.WithValue(ctx, rxUploadContextKey{}, r), srv.URL+c.GetNormalizedPath(), "session", strconv.Itoa(seq), buf.MergeBytes(nil, payload[offset:end])); err != nil {
					t.Fatal(err)
				}
				offset = end
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("bytes %d: %v", len(got), err)
			}
		})
	}
}

func TestRXAuthPayloadTamperAndReplayCapacity(t *testing.T) {
	digest := sha256.Sum256([]byte("authentic"))
	expected := hex.EncodeToString(digest[:])
	if !rxVerifyPayload(expected, []byte("authentic")) || rxVerifyPayload(expected, []byte("changed")) || rxVerifyPayload("", nil) {
		t.Fatal("payload binding failed")
	}
	c := rxAuthTestConfig()
	h := rxAuthTestHandler(c)
	for i := 0; i < 65; i++ {
		codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
		req, _ := rxAuthTestRequest(t, codec, "open")
		_, _, ok := h.authenticateRX(httptest.NewRecorder(), req, "session", "")
		if ok != (i < 64) {
			t.Fatalf("cache capacity index %d: %v", i, ok)
		}
	}
	if len(h.rxAuth.seen) != 64 {
		t.Fatal("unbounded replay cache")
	}
}

func TestRXAuthRejectsTamperedUploadBeforeDelivery(t *testing.T) {
	c := rxAuthTestConfig()
	c.UplinkDataPlacement = PlacementHeader
	c.UplinkDataKey = "data"
	h := rxAuthTestHandler(c)
	h.ln = &Listener{config: c, addConn: func(conn stat.Connection) {
		go func() {
			defer conn.Close()
			data := make([]byte, 9)
			if _, err := io.ReadFull(conn, data); err == nil {
				conn.Write(data)
			}
		}()
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	defer h.closeRX()
	opts, _ := c.NormalizeRX()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := newRXDownlink(ctx, c, opts, srv.URL+c.GetNormalizedPath(), "session", rxAuthTestClient)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	req, _ := http.NewRequest("POST", srv.URL+c.GetNormalizedPath(), nil)
	c.FillPacketRequest(req, "session", "0", buf.MergeBytes(nil, []byte("authentic")))
	digest := sha256.Sum256([]byte("authentic"))
	req.Header.Set(rxVersionHeader, "1")
	req.Header.Set(rxTokenHeader, r.token)
	req.Header.Set(rxActionHeader, "upload")
	req.Header.Set(rxNonceHeader, "0011223344556677")
	req.Header.Set(rxUploadHeader, "1")
	req.Header.Set(rxPayloadHashHeader, hex.EncodeToString(digest[:]))
	counter, err := r.auth.sealRequest(req, "session", "0")
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("data-0", base64.RawURLEncoding.EncodeToString([]byte("malicious")))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || r.auth.openResponse(resp, req.Method, req.URL.EscapedPath(), "session", "0", counter) != nil {
		t.Fatal("tampered upload not authentically refused")
	}
	client := &DefaultDialerClient{transportConfig: c}
	if err := client.PostPacket(context.WithValue(ctx, rxUploadContextKey{}, r), srv.URL+c.GetNormalizedPath(), "session", "0", buf.MergeBytes(nil, []byte("authentic"))); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "authentic" {
		t.Fatalf("tampering corrupted stream/cursor: %q %v", got, err)
	}
}

func TestRXAuthPublicHTTPRequiresKey(t *testing.T) {
	c := rxAuthTestConfig()
	c.Rx.AuthKey = ""
	if conn, err := Dial(context.Background(), net.TCPDestination(net.ParseAddress("192.0.2.1"), 80), rxStreamConfig(c, nil)); err == nil {
		conn.Close()
		t.Fatal("public HTTP dial leaked legacy token")
	}
	if listener, err := ListenXH(context.Background(), net.AnyIP, 0x7fff, rxStreamConfig(c, nil), func(stat.Connection) {}); err == nil {
		listener.Close()
		t.Fatal("public HTTP accepted missing key")
	}
	c.Rx.SpreadAddresses = []string{"192.0.2.1"}
	if conn, err := Dial(context.Background(), net.TCPDestination(net.LocalHostIP, 80), rxStreamConfig(c, nil)); err == nil {
		conn.Close()
		t.Fatal("public spread address bypassed loopback HTTP key guard")
	}
}

func BenchmarkRXAuthControl(b *testing.B) {
	c := rxAuthTestConfig()
	codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
	h := rxAuthTestHandler(c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := rxAuthTestRequest(b, codec, "segment")
		if _, _, ok := h.authenticateRX(httptest.NewRecorder(), req, "session", ""); !ok {
			b.Fatal("auth failed")
		}
	}
}

func TestRXAuthHTTP2AndWireBudget(t *testing.T) {
	c := rxAuthTestConfig()
	opts, _ := c.NormalizeRX()
	payload := bytes.Repeat([]byte("authenticated-h2"), 1024)
	h := rxAuthTestHandler(c)
	h.ln = &Listener{config: c, addConn: func(conn stat.Connection) { go func() { defer conn.Close(); conn.Write(payload) }() }}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	defer h.closeRX()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	factory := func() *http.Client {
		tr := srv.Client().Transport.(*http.Transport).Clone()
		tr.DisableKeepAlives = true
		return &http.Client{Transport: tr}
	}
	r, err := newRXDownlink(ctx, c, opts, srv.URL+c.GetNormalizedPath(), "session", factory)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("HTTP2 bytes %d: %v", len(got), err)
	}

	codec, _ := newRXClientAuth(c.Rx.AuthKey, "session")
	req, _ := http.NewRequest("GET", "http://example.test/secure/session", nil)
	for key, value := range map[string]string{rxVersionHeader: "1", rxTokenHeader: strings.Repeat("ab", 32), rxNonceHeader: "0011223344556677",
		rxActionHeader: "segment", rxOffsetHeader: "67108864", rxGenerationHeader: "1", rxSegmentHeader: "65536", rxParallelHeader: "16",
		rxAckHeader: "67043328", rxSegmentEndHeader: "67174400", rxUploadHeader: "1"} {
		req.Header.Set(key, value)
	}
	var before, after bytes.Buffer
	req.Header.Write(&before)
	if _, err := codec.sealRequest(req, "session", ""); err != nil {
		t.Fatal(err)
	}
	req.Header.Write(&after)
	if after.Len() >= before.Len() || bytes.Contains(after.Bytes(), []byte(strings.Repeat("ab", 32))) {
		t.Fatalf("wire controls grew/leaked: %d -> %d", before.Len(), after.Len())
	}
	t.Logf("segment control header bytes: %d -> %d", before.Len(), after.Len())
}
