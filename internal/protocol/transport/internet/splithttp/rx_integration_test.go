package splithttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	gotls "crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol/tls/cert"
	"github.com/roseforljh/RayBox-rx/internal/protocol/testing/servers/tcp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	xtls "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
	"golang.org/x/net/http2"
)

// rxTCPFaultProxy counts actual bytes on each accepted TCP connection, including
// HTTP headers and TLS records. It deliberately does not count HTTP requests or
// pretend that a Read/Write call corresponds to an IP packet. "freeze" stops
// forwarding downstream bytes without closing either socket; the uplink remains
// live until the client cancels it. This is a local TCP simulation, not a CDN or
// Russian-ISP measurement.
type rxTCPFaultProxy struct {
	listener  stdnet.Listener
	upstream  string
	threshold int64
	mode      string
	accepted  atomic.Uint64
	cuts      atomic.Uint64
	downBytes atomic.Uint64
	upBytes   atomic.Uint64
	mu        sync.Mutex
	conns     map[stdnet.Conn]struct{}
	closed    bool
	wg        sync.WaitGroup
}

func TestRXUploadAcceptedBeforeQueueClose(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	synctest.Test(t, func(t *testing.T) {
		q := NewUploadQueue(0)
		result := make(chan error, 1)
		go func() {
			result <- q.push(context.Background(), Packet{Payload: []byte("final"), Seq: 0})
		}()
		synctest.Wait() // Hold the sender until the application consumes its final packet.
		data := make([]byte, 5)
		if n, err := q.Read(data); err != nil || n != len(data) || string(data) != "final" {
			t.Fatalf("final packet: %q, %d, %v", data, n, err)
		}
		q.Close()
		if err := <-result; err != nil {
			t.Fatalf("consumed upload reported as failed after close: %v", err)
		}
		if err := q.push(context.Background(), Packet{Seq: 1}); err == nil {
			t.Fatal("upload accepted after queue closed")
		}
	})
}

func newRXTCPFaultProxy(t *testing.T, upstream, mode string, threshold int64) *rxTCPFaultProxy {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &rxTCPFaultProxy{listener: ln, upstream: upstream, mode: mode, threshold: threshold, conns: make(map[stdnet.Conn]struct{})}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				c.Close()
				return
			}
			p.conns[c] = struct{}{}
			p.wg.Add(1)
			p.mu.Unlock()
			p.accepted.Add(1)
			go p.serve(c)
		}
	}()
	t.Cleanup(p.close)
	return p
}

type rxCountWriter struct {
	io.Writer
	counter *atomic.Uint64
}

func (w rxCountWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.counter.Add(uint64(n))
	return n, err
}

func (p *rxTCPFaultProxy) serve(c stdnet.Conn) {
	defer p.wg.Done()
	defer func() { c.Close(); p.mu.Lock(); delete(p.conns, c); p.mu.Unlock() }()
	u, err := stdnet.DialTimeout("tcp", p.upstream, time.Second)
	if err != nil {
		return
	}
	defer u.Close()
	done := make(chan struct{})
	go func() {
		io.Copy(rxCountWriter{u, &p.upBytes}, c)
		u.Close()
		close(done)
	}()
	defer func() { c.Close(); u.Close(); <-done }()
	w := rxCountWriter{c, &p.downBytes}
	if p.threshold == 0 {
		io.Copy(w, u)
		return
	}
	if _, err := io.CopyN(w, u, p.threshold); err != nil {
		return
	}
	p.cuts.Add(1)
	switch p.mode {
	case "freeze":
		<-done
	case "rst":
		c.(*stdnet.TCPConn).SetLinger(0)
	case "fin":
		c.(*stdnet.TCPConn).CloseWrite()
	}
}

func (p *rxTCPFaultProxy) close() {
	p.mu.Lock()
	p.closed = true
	p.listener.Close()
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func rxIntegrationConfig(enabled bool) *Config {
	c := &Config{
		Path: "/rx-integration", Mode: "packet-up", Host: "localhost",
		XPaddingBytes:        &RangeConfig{From: 100, To: 100},
		ScMaxEachPostBytes:   &RangeConfig{From: 4096, To: 4096},
		ScMinPostsIntervalMs: &RangeConfig{From: 1, To: 1},
	}
	if enabled {
		c.Rx = &RXConfig{
			Enabled: true, StallDetect: true, MaxReplayBytes: 262144,
			ProbeIntervalMs: 20, StallTimeoutMs: 80, RequestTimeoutMs: 500,
			SessionTtlSecs: 5, MaxReconnects: 64, RetryDelayMs: 1,
		}
	}
	return c
}

func rxStreamConfig(c *Config, security *xtls.Config) *internet.MemoryStreamConfig {
	s := &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: c}
	if security != nil {
		s.SecurityType = "tls"
		s.SecuritySettings = security
	}
	return s
}

func rxListen(t *testing.T, c *Config, security *xtls.Config, handle func(stat.Connection)) internet.Listener {
	t.Helper()
	ln, err := ListenXH(context.Background(), net.LocalHostIP, tcp.PickPort(), rxStreamConfig(c, security), func(c stat.Connection) { go handle(c) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func rxDial(t *testing.T, address string, c *Config, security *xtls.Config) stat.Connection {
	t.Helper()
	host, port, err := stdnet.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	p, err := net.PortFromString(port)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	conn, err := Dial(ctx, net.TCPDestination(net.ParseAddress(host), p), rxStreamConfig(c, security))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestXHTTPFingerprintHonorsALPN(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
			serverTLS := &xtls.Config{Certificate: []*xtls.Certificate{xtls.ParseCertificate(ct)}, NextProtocol: []string{"h2", "http/1.1"}}
			ln := rxListen(t, rxIntegrationConfig(false), serverTLS, func(c stat.Connection) { c.Close() })
			host, port, err := stdnet.SplitHostPort(ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			p, err := net.PortFromString(port)
			if err != nil {
				t.Fatal(err)
			}
			clientTLS := &xtls.Config{ServerName: "localhost", PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{protocol}, Fingerprint: "chrome"}
			client := createHTTPClient(net.TCPDestination(net.ParseAddress(host), p), rxStreamConfig(rxIntegrationConfig(false), clientTLS), context.Background()).(*DefaultDialerClient)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := client.dialUploadConn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			state := conn.(interface{ ConnectionState() gotls.ConnectionState }).ConnectionState()
			if !state.HandshakeComplete || state.NegotiatedProtocol != protocol {
				t.Fatalf("configured %q, negotiated %q", protocol, state.NegotiatedProtocol)
			}
		})
	}
}

func rxPayload() []byte {
	b := make([]byte, 8192)
	for i := range b {
		b[i] = byte((i*73 + i/17 + 29) % 251)
	}
	return b
}

func rxWritePayload(w io.Writer, size int64) error {
	b := rxPayload()
	for size > 0 {
		n := min(size, int64(len(b)))
		written, err := w.Write(b[:n])
		if err != nil {
			return err
		}
		if int64(written) != n {
			return io.ErrShortWrite
		}
		size -= n
	}
	return nil
}

func rxExpectedHash(size int64) []byte {
	h := sha256.New()
	if err := rxWritePayload(h, size); err != nil {
		panic(err)
	}
	return h.Sum(nil)
}

func rxReadHash(t *testing.T, c stat.Connection, size int64) {
	t.Helper()
	h := sha256.New()
	n, err := io.Copy(h, c)
	if err != nil {
		t.Fatalf("download at %d/%d: %v", n, size, err)
	}
	if n != size || !bytes.Equal(h.Sum(nil), rxExpectedHash(size)) {
		t.Fatalf("integrity failure: bytes=%d expected=%d sha256=%x expected_sha256=%x", n, size, h.Sum(nil), rxExpectedHash(size))
	}
}

func rxPercentile(v []time.Duration, p float64) time.Duration {
	if len(v) == 0 {
		return 0
	}
	copyV := append([]time.Duration(nil), v...)
	sort.Slice(copyV, func(i, j int) bool { return copyV[i] < copyV[j] })
	return copyV[min(len(copyV)-1, int(float64(len(copyV)-1)*p))]
}

// These are whole-test-process Go memory measurements, not per-server RSS.
// The process includes client, server, TCP proxy and test bookkeeping.
func rxMemorySample() func() map[string]uint64 {
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	result := make(map[string]uint64)
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			result["peak_heap_alloc_bytes"] = max(result["peak_heap_alloc_bytes"], m.HeapAlloc)
			result["peak_heap_inuse_bytes"] = max(result["peak_heap_inuse_bytes"], m.HeapInuse)
			result["peak_go_sys_bytes"] = max(result["peak_go_sys_bytes"], m.Sys)
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() map[string]uint64 { once.Do(func() { close(stop) }); <-done; return result }
}

func rxSaveMetrics(t *testing.T, values map[string]any) {
	t.Helper()
	values["scope"] = "local simulation; client+server+TCP proxy+tests in one process; wire bytes exclude TCP/IP headers"
	b, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("XRAY_RX_METRICS_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		name := strings.ReplaceAll(t.Name(), "/", "_") + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rxRunFaultDownload(t *testing.T, mode string, size int64, h2 bool) {
	t.Helper()
	finishMemory := rxMemorySample()
	t.Cleanup(func() { finishMemory() })
	var security *xtls.Config
	if h2 {
		ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
		security = &xtls.Config{Certificate: []*xtls.Certificate{xtls.ParseCertificate(ct)}, PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{"h2"}}
	}
	serverErr := make(chan error, 1)
	ln := rxListen(t, rxIntegrationConfig(true), security, func(c stat.Connection) {
		defer c.Close()
		var start [1]byte
		if _, err := io.ReadFull(c, start[:]); err != nil {
			serverErr <- err
			return
		}
		serverErr <- rxWritePayload(c, size)
	})
	threshold := int64(16 << 10)
	proxy := newRXTCPFaultProxy(t, ln.Addr().String(), mode, threshold)
	start := time.Now()
	cc := rxIntegrationConfig(true)
	if size >= 50<<20 {
		cc.Rx.MaxReconnects = 8192 // explicit long-run budget, not production default
	}
	c := rxDial(t, proxy.listener.Addr().String(), cc, security)
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	rxReadHash(t, c, size)
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if proxy.cuts.Load() == 0 {
		t.Fatal("fault was not injected")
	}
	r, ok := c.(*splitConn).reader.(*rxDownlink)
	if !ok {
		t.Fatalf("expected RX reader, got %T", c.(*splitConn).reader)
	}
	s := r.statsSnapshot()
	if s.Recoveries == 0 {
		t.Fatal("fault injected but no recovery recorded")
	}
	if mode == "freeze" && s.ConfirmedStalls == 0 {
		t.Fatal("silent freeze not detected")
	}
	t.Logf("LOCAL_TCP mode=%s h2=%v threshold_wire_bytes=%d bytes=%d sha256=%x elapsed=%s throughput_MiBps=%.3f tcp_connections=%d connections_per_MiB=%.2f cuts=%d recoveries=%d attempts=%d success_ratio=%.5f recovery_p50=%s recovery_p95=%s client_wire_down=%d client_wire_up=%d replay_limit=%d",
		mode, h2, threshold, size, rxExpectedHash(size), elapsed, float64(size)/(1<<20)/elapsed.Seconds(), proxy.accepted.Load(), float64(proxy.accepted.Load())/(float64(size)/(1<<20)), proxy.cuts.Load(), s.Recoveries, s.ReconnectAttempts, float64(s.Recoveries)/float64(s.ReconnectAttempts), rxPercentile(s.RecoveryDurations, .5), rxPercentile(s.RecoveryDurations, .95), proxy.downBytes.Load(), proxy.upBytes.Load(), 262144)
	rxSaveMetrics(t, map[string]any{
		"fault": mode, "h2_tls": h2, "threshold_wire_bytes": threshold, "payload_bytes": size, "sha256": fmt.Sprintf("%x", rxExpectedHash(size)),
		"elapsed_seconds": elapsed.Seconds(), "throughput_mib_per_second": float64(size) / (1 << 20) / elapsed.Seconds(), "tcp_connections": proxy.accepted.Load(),
		"cuts": proxy.cuts.Load(), "recoveries": s.Recoveries, "reconnect_attempts": s.ReconnectAttempts,
		"recovery_p50_ms": float64(rxPercentile(s.RecoveryDurations, .5)) / float64(time.Millisecond), "recovery_p95_ms": float64(rxPercentile(s.RecoveryDurations, .95)) / float64(time.Millisecond),
		"recovery_definition":    "from client error observation/stall trigger until first delivered byte on resumed connection; excludes time before detection",
		"client_wire_down_bytes": proxy.downBytes.Load(), "client_wire_up_bytes": proxy.upBytes.Load(), "replay_limit_bytes": 262144, "go_memory": finishMemory(),
	})
}

func TestRXIntegrationTCPFaults(t *testing.T) {
	for _, mode := range []string{"fin", "rst", "freeze"} {
		t.Run(mode, func(t *testing.T) { rxRunFaultDownload(t, mode, 256<<10, false) })
	}
	t.Run("h2_fin", func(t *testing.T) { rxRunFaultDownload(t, "fin", 256<<10, true) })
}

func TestRXIntegrationSegments(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2_%v", h2), func(t *testing.T) {
			protocol := "http/1.1"
			if h2 {
				protocol = "h2"
			}
			ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
			security := &xtls.Config{Certificate: []*xtls.Certificate{xtls.ParseCertificate(ct)}, PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{protocol}, Fingerprint: "unsafe"}
			sc, cc := rxIntegrationConfig(true), rxIntegrationConfig(true)
			for _, cfg := range []*Config{sc, cc} {
				cfg.Rx.StallDetect = false
				cfg.Rx.MaxResponseBytes = 1024
				cfg.Rx.Preconnects = 2
				cfg.Rx.MaxSegments = 128
				cfg.Rx.RequestTimeoutMs = 2000
				cfg.Rx.ProbeIntervalMs = 100
				cfg.Rx.StallTimeoutMs = 100
				cfg.UplinkHTTPMethod, cfg.UplinkDataPlacement, cfg.UplinkDataKey = "GET", PlacementHeader, "data"
			}
			const size = 32<<10 + 317
			serverErr := make(chan error, 1)
			ln := rxListen(t, sc, security, func(c stat.Connection) {
				defer c.Close()
				// No response bytes until the first upload: open must flush headers.
				var start [1]byte
				if _, err := io.ReadFull(c, start[:]); err != nil {
					serverErr <- err
					return
				}
				writeDone := make(chan error, 1)
				go func() { writeDone <- rxWritePayload(c, size) }()
				got := make([]byte, 1024)
				_, err := io.ReadFull(c, got)
				if err == nil && !bytes.Equal(got, rxPayload()[:1024]) {
					err = fmt.Errorf("segmented bidirectional upload corrupted")
				}
				if writeErr := <-writeDone; err == nil {
					err = writeErr
				}
				serverErr <- err
			})
			proxy := newRXTCPFaultProxy(t, ln.Addr().String(), "freeze", 16<<10)
			c := rxDial(t, proxy.listener.Addr().String(), cc, security)
			if _, err := c.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			uploadDone := make(chan error, 1)
			go func() {
				for _, p := range [][]byte{rxPayload()[:512], rxPayload()[512:1024]} {
					if _, err := c.Write(p); err != nil {
						uploadDone <- err
						return
					}
				}
				uploadDone <- nil
			}()
			rxReadHash(t, c, size)
			if err := <-uploadDone; err != nil {
				t.Fatal(err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
			r := c.(*splitConn).reader.(*rxDownlink)
			s := r.statsSnapshot()
			if r.segment != 1024 || s.Segments < 32 || s.ReconnectAttempts != 0 || s.Recoveries != 0 || s.ConfirmedStalls != 0 {
				t.Fatalf("normal segments classified as recovery: %+v", s)
			}
			if proxy.accepted.Load() < s.SegmentRequests+s.UploadRequests {
				t.Fatalf("requests reused TCP: tcp=%d data=%d upload=%d", proxy.accepted.Load(), s.SegmentRequests, s.UploadRequests)
			}
			t.Logf("LOCAL segments=%d data_GET=%d uploads=%d TCP=%d cuts=%d bytes=%d sha256=%x", s.Segments, s.SegmentRequests, s.UploadRequests, proxy.accepted.Load(), proxy.cuts.Load(), size, rxExpectedHash(size))
		})
	}
}

func TestRXSegmentH2NoImplicitRetry(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused_%v", refused), func(t *testing.T) {
			client, server := stdnet.Pipe()
			defer server.Close()
			var dials atomic.Uint64
			pool := &rxPreconnect{version: "2", opts: rxTestClientOptions(), dial: func(context.Context) (stdnet.Conn, error) {
				dials.Add(1)
				return nil, fmt.Errorf("unexpected second TCP")
			}, queue: []rxWarmConn{{client, time.Now()}}}
			defer pool.close()
			serverDone := make(chan error, 1)
			go func() {
				defer server.Close()
				preface := make([]byte, len(http2.ClientPreface))
				if _, err := io.ReadFull(server, preface); err != nil {
					serverDone <- err
					return
				}
				fr := http2.NewFramer(server, server)
				var streamID uint32
				var settingsAck bool
				for {
					frame, err := fr.ReadFrame()
					if err != nil {
						serverDone <- err
						return
					}
					switch frame := frame.(type) {
					case *http2.WindowUpdateFrame:
						if err := fr.WriteSettings(); err != nil {
							serverDone <- err
							return
						}
					case *http2.SettingsFrame:
						settingsAck = frame.IsAck()
					case *http2.HeadersFrame:
						streamID = frame.StreamID
					}
					// Drain the SETTINGS ACK before writing the fault: net.Pipe has
					// no buffer, so simultaneous writes would block both peers.
					if settingsAck && streamID != 0 {
						if refused {
							err = fr.WriteRSTStream(streamID, http2.ErrCodeRefusedStream)
						} else {
							err = fr.WriteGoAway(0, http2.ErrCodeNo, nil)
						}
						serverDone <- err
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://localhost/", nil)
			transport := &rxOneShotTransport{pool: pool}
			defer transport.CloseIdleConnections()
			resp, err := transport.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
				t.Fatal("expected explicit H2 error")
			}
			if refused {
				var streamErr http2.StreamError
				if !errors.As(err, &streamErr) || streamErr.StreamID != 1 || streamErr.Code != http2.ErrCodeRefusedStream {
					t.Fatalf("expected REFUSED_STREAM, got %v", err)
				}
			} else if !strings.Contains(err.Error(), "GOAWAY") {
				t.Fatalf("expected GOAWAY, got %v", err)
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
			if dials.Load() != 0 {
				t.Fatalf("implicit redial: %d", dials.Load())
			}
			if _, err := transport.RoundTrip(req); err == nil {
				t.Fatal("transport accepted second request")
			}
		})
	}
}

func TestRXPreconnectLifecycle(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2_%v", h2), func(t *testing.T) {
			ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			ct, _ := cert.MustGenerate(nil, cert.CommonName("localhost"))
			certPEM, keyPEM := ct.ToPEM()
			pair, err := gotls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				t.Fatal(err)
			}
			protocol, version := "http/1.1", "1.1"
			if h2 {
				protocol, version = "h2", "2"
			}
			serverConfig := &gotls.Config{Certificates: []gotls.Certificate{pair}, NextProtos: []string{protocol}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := rxTestClientOptions()
			opts.Preconnects, opts.RequestTimeoutMs = 2, 2000
			pool := &rxPreconnect{version: version, opts: opts, dial: func(ctx context.Context) (stdnet.Conn, error) {
				return (&gotls.Dialer{Config: &gotls.Config{InsecureSkipVerify: true, NextProtos: []string{protocol}}}).DialContext(ctx, "tcp", ln.Addr().String())
			}}
			pool.start(ctx)
			peers := make([]stdnet.Conn, 0, 2)
			defer func() {
				for _, c := range peers {
					c.Close()
				}
			}()
			for i := 0; i < 2; i++ {
				ln.(*stdnet.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
				raw, err := ln.Accept()
				if err != nil {
					t.Fatal(err)
				}
				c := gotls.Server(raw, serverConfig)
				if err := c.HandshakeContext(ctx); err != nil {
					t.Fatal(err)
				}
				peers = append(peers, c)
				c.SetReadDeadline(time.Now().Add(15 * time.Millisecond))
				var b [1]byte
				if n, err := c.Read(b[:]); n != 0 || err == nil {
					t.Fatalf("preconnect sent HTTP bytes: n=%d err=%v", n, err)
				}
				c.SetReadDeadline(time.Time{})
			}
			cancel()
			pool.close()
			for _, c := range peers {
				c.SetReadDeadline(time.Now().Add(time.Second))
				var b [1]byte
				if n, err := c.Read(b[:]); n != 0 || err == nil {
					t.Fatalf("queued connection leaked: n=%d err=%v", n, err)
				}
			}
		})
	}
	t.Run("alpn-mismatch", func(t *testing.T) {
		client, server := stdnet.Pipe()
		defer server.Close()
		pool := &rxPreconnect{version: "2", opts: rxTestClientOptions(), dial: func(context.Context) (stdnet.Conn, error) { return client, nil }}
		if c, err := pool.connect(context.Background()); err == nil {
			c.Close()
			t.Fatal("H2 accepted missing ALPN")
		}
		server.SetReadDeadline(time.Now().Add(time.Second))
		var p [1]byte
		if _, err := server.Read(p[:]); err != io.EOF {
			t.Fatalf("ALPN failure left TCP open: %v", err)
		}
	})
	t.Run("bounded-failures", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		opts := rxTestClientOptions()
		opts.Preconnects, opts.MaxReconnects = 2, 2
		var attempts atomic.Int32
		pool := &rxPreconnect{opts: opts, dial: func(context.Context) (stdnet.Conn, error) {
			attempts.Add(1)
			return nil, fmt.Errorf("offline")
		}}
		pool.fill(ctx)
		if attempts.Load() != 2 {
			t.Fatalf("prewarm attempts=%d", attempts.Load())
		}
	})
	t.Run("standby-plus-active", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		opts := rxTestClientOptions()
		opts.Preconnects = 1
		started, finished := make(chan struct{}, 2), make(chan struct{}, 2)
		var calls atomic.Int32
		pool := &rxPreconnect{version: "1.1", opts: opts, dial: func(ctx context.Context) (stdnet.Conn, error) {
			calls.Add(1)
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return nil, ctx.Err()
		}}
		pool.start(ctx)
		<-started // one background handshake owns the only standby slot
		activeDone := make(chan struct{})
		go func() { pool.take(ctx); close(activeDone) }()
		<-started // the single active GET may cold-dial outside standby K
		if calls.Load() != 2 {
			t.Fatalf("unexpected total dials: %d", calls.Load())
		}
		cancel()
		for i := 0; i < 2; i++ {
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("dial not canceled")
			}
		}
		<-activeDone
	})
	t.Run("cancel-inflight", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started, done := make(chan struct{}), make(chan struct{})
		opts := rxTestClientOptions()
		opts.Preconnects = 2
		pool := &rxPreconnect{opts: opts, dial: func(ctx context.Context) (stdnet.Conn, error) {
			close(started)
			<-ctx.Done()
			close(done)
			return nil, ctx.Err()
		}}
		pool.start(ctx)
		<-started
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("handshake context leaked")
		}
	})
}

func TestRXIntegration50MiB(t *testing.T) {
	if os.Getenv("XRAY_RX_LONG_TEST") != "1" {
		t.Skip("set XRAY_RX_LONG_TEST=1 for 50MiB through a 16KiB/TCP FIN injector")
	}
	rxRunFaultDownload(t, "fin", 50<<20, false)
}

func TestRXIntegrationCompatibility(t *testing.T) {
	for _, clientEnabled := range []bool{false, true} {
		for _, serverEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("client_%v_server_%v", clientEnabled, serverEnabled), func(t *testing.T) {
				ln := rxListen(t, rxIntegrationConfig(serverEnabled), nil, func(c stat.Connection) {
					defer c.Close()
					var b [1]byte
					if _, err := io.ReadFull(c, b[:]); err == nil {
						rxWritePayload(c, 32<<10)
					}
				})
				c := rxDial(t, ln.Addr().String(), rxIntegrationConfig(clientEnabled), nil)
				if _, err := c.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				rxReadHash(t, c, 32<<10)
			})
		}
	}
}

func TestRXIntegrationIdleIsNotStall(t *testing.T) {
	ln := rxListen(t, rxIntegrationConfig(true), nil, func(c stat.Connection) {
		defer c.Close()
		var b [1]byte
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return
		}
		time.Sleep(400 * time.Millisecond) // five stall timeouts, but no pending data
		rxWritePayload(c, 1024)
	})
	c := rxDial(t, ln.Addr().String(), rxIntegrationConfig(true), nil)
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	rxReadHash(t, c, 1024)
	s := c.(*splitConn).reader.(*rxDownlink).statsSnapshot()
	if s.ConfirmedStalls != 0 || s.Recoveries != 0 {
		t.Fatalf("idle misclassified: %+v", s)
	}
}

func TestRXIntegrationGETHeaderHasNoBody(t *testing.T) {
	sc, cc := rxIntegrationConfig(true), rxIntegrationConfig(true)
	for _, c := range []*Config{sc, cc} {
		c.UplinkHTTPMethod = "GET"
		c.UplinkDataPlacement = PlacementHeader
		c.UplinkDataKey = "data"
	}
	ln := rxListen(t, sc, nil, func(c stat.Connection) { defer c.Close(); io.Copy(c, c) })
	target, _ := url.Parse("http://" + ln.Addr().String())
	reverse := httputil.NewSingleHostReverseProxy(target)
	var uploads atomic.Int64
	bad := make(chan string, 1)
	recordBad := func(s string) {
		select {
		case bad <- s:
		default:
		}
	}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, seq := sc.ExtractMetaFromRequest(r, sc.GetNormalizedPath())
		if seq != "" {
			b, err := io.ReadAll(r.Body)
			if err != nil || len(b) != 0 || r.ContentLength > 0 || r.Method != "GET" || r.Header.Get("data-0") == "" {
				recordBad(fmt.Sprintf("method=%s contentLength=%d bodyBytes=%d header=%v err=%v", r.Method, r.ContentLength, len(b), r.Header.Get("data-0") != "", err))
			}
			r.Body = http.NoBody
			uploads.Add(1)
		}
		reverse.ServeHTTP(w, r)
	}))
	defer bridge.Close()
	c := rxDial(t, bridge.Listener.Addr().String(), cc, nil)
	payload := rxPayload()
	writeErr := make(chan error, 1)
	go func() { _, err := c.Write(payload); writeErr <- err }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, got) {
		t.Fatal("GET header echo corrupted")
	}
	c.Close()
	if uploads.Load() < 2 {
		t.Fatalf("expected multiple header uplinks, got %d", uploads.Load())
	}
	select {
	case msg := <-bad:
		t.Fatal(msg)
	default:
	}
}

func TestRXUplinkReliability(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry-h2-%v", h2), func(t *testing.T) {
			cfg := rxIntegrationConfig(true)
			cfg.UplinkHTTPMethod, cfg.UplinkDataPlacement, cfg.UplinkDataKey = "GET", PlacementHeader, "data"
			opts, err := cfg.NormalizeRX()
			if err != nil {
				t.Fatal(err)
			}
			opts.ProbeIntervalMs = 30000
			first := []byte("a-packet-read-in-small-pieces")
			last := []byte("next-packet")
			partial, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseReader := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseReader)
			received := make(chan []byte, 1)
			h := &requestHandler{config: cfg, path: cfg.GetNormalizedPath(), sessionMu: &sync.Mutex{}}
			h.ln = &Listener{config: cfg, addConn: func(c stat.Connection) {
				go func() {
					p := make([]byte, len(first)+len(last))
					if _, err := io.ReadFull(c, p[:3]); err != nil {
						received <- nil
						return
					}
					close(partial)
					<-release
					_, err := io.ReadFull(c, p[3:])
					if err != nil {
						p = nil
					}
					received <- p
				}()
			}}
			var attempts atomic.Int32
			var mu sync.Mutex
			peers := map[string]bool{}
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get(rxActionHeader) != "upload" {
					h.ServeHTTP(w, req)
					return
				}
				mu.Lock()
				peers[req.RemoteAddr] = true
				mu.Unlock()
				if (req.ProtoMajor == 2) != h2 {
					t.Error("unexpected HTTP version", req.Proto)
				}
				n := attempts.Add(1)
				if n == 1 {
					panic(http.ErrAbortHandler) // request never reached the application
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				if n == 2 {
					select {
					case <-partial:
					case <-req.Context().Done():
					}
					panic(http.ErrAbortHandler) // accepted, partly consumed, ACK lost
				}
				for k, v := range rr.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(rr.Code)
			}))
			srv.EnableHTTP2 = h2
			if h2 {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			t.Cleanup(func() { h.closeRX(); srv.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			factory := func() *http.Client { return &http.Client{Transport: srv.Client().Transport.(*http.Transport).Clone()} }
			r, err := newRXDownlink(ctx, cfg, opts, srv.URL+cfg.GetNormalizedPath(), "upload-test", factory)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if !r.uploadRetry {
				t.Fatal("upload retry not negotiated")
			}
			client := &DefaultDialerClient{transportConfig: cfg}
			uploadCtx := context.WithValue(r.ctx, rxUploadContextKey{}, r)
			for i, data := range [][]byte{first, last} {
				if err := client.PostPacket(uploadCtx, srv.URL+cfg.GetNormalizedPath(), r.sessionID, strconv.Itoa(i), buf.MergeBytes(nil, data)); err != nil {
					t.Fatal(err)
				}
			}
			releaseReader()
			select {
			case got := <-received:
				if !bytes.Equal(got, append(append([]byte(nil), first...), last...)) {
					t.Fatalf("duplicate or lost bytes: %q", got)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			mu.Lock()
			connections := len(peers)
			mu.Unlock()
			if attempts.Load() != 4 || connections != 4 || r.statsSnapshot().UploadRetries != 2 {
				t.Fatalf("requests=%d connections=%d stats=%+v", attempts.Load(), connections, r.statsSnapshot())
			}
			h.sessionMu.Lock()
			s := h.rxSessions[r.sessionID]
			h.sessionMu.Unlock()
			s.legacy.uploadQueue.Close()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			req.Header.Set(rxNonceHeader, "0011223344556677")
			for _, test := range []struct {
				seq  uint64
				p    []byte
				code int
			}{{1, last, 200}, {1, []byte("different"), 409}, {0, first, 409}, {3, last, 409}, {2, last, 503}} {
				rr := httptest.NewRecorder()
				h.pushRXUpload(rr, req, s, test.seq, test.p)
				if rr.Code != test.code {
					t.Fatalf("seq=%d status=%d want=%d", test.seq, rr.Code, test.code)
				}
			}
		})
	}
	t.Run("old-rx-peer", func(t *testing.T) {
		var uploads, redirected atomic.Int32
		var redirect atomic.Bool
		sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
		defer sink.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get(rxActionHeader) == "upload" {
				uploads.Add(1)
				if req.Header.Get(rxUploadHeader) != "" {
					t.Error("sent negotiated-only upload to old peer")
				}
				if redirect.Load() {
					http.Redirect(w, req, sink.URL, http.StatusFound)
				}
				return
			}
			rxTestConfirm(w, req, 0, false)
			if req.Header.Get(rxActionHeader) == "open" {
				w.(http.Flusher).Flush()
				<-req.Context().Done()
			}
		}))
		defer srv.Close()
		opts := rxTestClientOptions()
		opts.ProbeIntervalMs = 30000
		r := rxTestNewClient(t, context.Background(), srv, opts)
		defer r.Close()
		if r.uploadRetry {
			t.Fatal("old peer enabled upload retry")
		}
		cfg := &Config{UplinkHTTPMethod: "GET", UplinkDataPlacement: PlacementHeader}
		client := &DefaultDialerClient{transportConfig: cfg, client: srv.Client()}
		ctx := context.WithValue(r.ctx, rxUploadContextKey{}, r)
		if err := client.PostPacket(ctx, srv.URL, r.sessionID, "0", buf.MergeBytes(nil, []byte("old"))); err != nil || uploads.Load() != 1 {
			t.Fatal("old RX behavior changed", err)
		}
		redirect.Store(true)
		if err := client.PostPacket(ctx, srv.URL, r.sessionID, "1", buf.MergeBytes(nil, []byte("private"))); err == nil || uploads.Load() != 2 || redirected.Load() != 0 {
			t.Fatal("old RX upload followed redirect or retried", err)
		}
	})
	t.Run("native-tls-cancel", func(t *testing.T) {
		ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		closed := make(chan bool, 1)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				closed <- false
				return
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(time.Second))
			_, err = io.Copy(io.Discard, conn) // accept TCP, never send a ServerHello
			closed <- err == nil
		}()
		host, port, _ := stdnet.SplitHostPort(ln.Addr().String())
		p, _ := net.PortFromString(port)
		security := &xtls.Config{ServerName: "localhost", NextProtocol: []string{"http/1.1"}, Fingerprint: "unsafe"}
		cfg := rxIntegrationConfig(true)
		opts := rxTestClientOptions()
		opts.RequestTimeoutMs = 30
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &rxDownlink{ctx: ctx, cancel: cancel, opts: opts, token: strings.Repeat("12", 32), factory: func() *http.Client {
			return createHTTPClient(net.TCPDestination(net.ParseAddress(host), p), rxStreamConfig(cfg, security), context.Background()).(*DefaultDialerClient).client
		}}
		client := &DefaultDialerClient{transportConfig: cfg}
		if err := client.PostPacket(context.WithValue(ctx, rxUploadContextKey{}, r), "https://localhost/", "sid", "0", buf.MergeBytes(nil, []byte("old"))); err == nil {
			t.Fatal("old RX TLS handshake ignored request timeout")
		}
		if !<-closed {
			t.Fatal("cancelled TLS TCP stayed open")
		}
	})
	t.Run("ordinary-native-tls", func(t *testing.T) {
		ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		accepted := make(chan stdnet.Conn, 1)
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				accepted <- conn
			}
		}()
		host, port, _ := stdnet.SplitHostPort(ln.Addr().String())
		p, _ := net.PortFromString(port)
		security := &xtls.Config{ServerName: "localhost", NextProtocol: []string{"http/1.1"}, Fingerprint: "unsafe"}
		client := createHTTPClient(net.TCPDestination(net.ParseAddress(host), p), rxStreamConfig(rxIntegrationConfig(false), security), context.Background()).(*DefaultDialerClient)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := client.dialUploadConn(ctx)
		if conn != nil {
			defer conn.Close()
		}
		if err != nil {
			t.Fatal("ordinary TLS dialing performed a handshake", err)
		}
		select {
		case peer := <-accepted:
			peer.Close()
		case <-ctx.Done():
			t.Fatal("ordinary TLS did not connect")
		}
	})
	t.Run("limits-and-confirmation", func(t *testing.T) {
		var requests atomic.Int32
		mode := atomic.Int32{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			requests.Add(1)
			switch mode.Load() {
			case 0:
				w.WriteHeader(503)
			case 1:
				<-req.Context().Done()
			default:
				w.WriteHeader(200) // a missing ACK is fatal, never a silent fallback
			}
		}))
		defer srv.Close()
		opts := rxTestClientOptions()
		opts.MaxReconnects = 2
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &rxDownlink{ctx: ctx, cancel: cancel, opts: opts, uploadRetry: true, token: strings.Repeat("12", 32), factory: func() *http.Client { return &http.Client{Transport: srv.Client().Transport.(*http.Transport).Clone()} }}
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		if err := r.postPacket(req, "0"); err == nil || requests.Load() != 3 || r.retryAttempts != 2 {
			t.Fatalf("budget error=%v requests=%d retries=%d", err, requests.Load(), r.retryAttempts)
		}
		r.mu.Lock()
		err := r.reserveRetryLocked(false)
		r.mu.Unlock()
		if err == nil {
			t.Fatal("downlink did not share upload retry budget")
		}
		mode.Store(2)
		before := requests.Load()
		if err := r.postPacket(req, "1"); err == nil || requests.Load() != before+1 {
			t.Fatal("missing ACK retried or accepted", err)
		}
		mode.Store(1)
		cancelCtx, cancelUpload := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancelUpload()
		if err := r.postPacket(req.Clone(cancelCtx), "2"); err == nil || cancelCtx.Err() == nil {
			t.Fatal("upload cancellation ignored", err)
		}
		q := NewUploadQueue(1)
		defer q.Close()
		q.Push(Packet{Seq: 0})
		if err := q.push(cancelCtx, Packet{Seq: 1}); err == nil || len(q.pushedPackets) != 1 {
			t.Fatal("cancelled queue push accepted", err)
		}
	})
}

func TestRXIntegrationConcurrentDownloads(t *testing.T) {
	const sessions = 4
	ln := rxListen(t, rxIntegrationConfig(true), nil, func(c stat.Connection) {
		defer c.Close()
		var b [1]byte
		if _, err := io.ReadFull(c, b[:]); err == nil {
			rxWritePayload(c, 128<<10)
		}
	})
	proxy := newRXTCPFaultProxy(t, ln.Addr().String(), "fin", 20<<10)
	for i := range sessions {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			c := rxDial(t, proxy.listener.Addr().String(), rxIntegrationConfig(true), nil)
			if _, err := c.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			rxReadHash(t, c, 128<<10)
		})
	}
}

func TestRXIntegrationUploadWhileDownlinkResumes(t *testing.T) {
	const uploadBytes = 512 << 10
	const downloadBytes = uploadBytes / (32 << 10) * 8192
	serverResult := make(chan error, 1)
	ln := rxListen(t, rxIntegrationConfig(true), nil, func(c stat.Connection) {
		defer c.Close()
		h := sha256.New()
		b := make([]byte, 32<<10)
		for remaining := uploadBytes; remaining > 0; remaining -= len(b) {
			if _, err := io.ReadFull(c, b); err != nil {
				serverResult <- err
				return
			}
			h.Write(b)
			if err := rxWritePayload(c, 8192); err != nil {
				serverResult <- err
				return
			}
		}
		if !bytes.Equal(h.Sum(nil), rxExpectedHash(uploadBytes)) {
			serverResult <- fmt.Errorf("upload hash mismatch: %x", h.Sum(nil))
			return
		}
		serverResult <- nil
	})
	proxy := newRXTCPFaultProxy(t, ln.Addr().String(), "fin", 16<<10)
	c := rxDial(t, proxy.listener.Addr().String(), rxIntegrationConfig(true), nil)
	uploadResult := make(chan error, 1)
	go func() { uploadResult <- rxWritePayload(c, uploadBytes) }()
	rxReadHash(t, c, downloadBytes)
	if err := <-uploadResult; err != nil {
		t.Fatal(err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	if proxy.cuts.Load() == 0 {
		t.Fatal("no fault during simultaneous upload/download")
	}
}

func TestRXIntegrationCannotRecoverBelowHandshakeBudget(t *testing.T) {
	ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
	security := &xtls.Config{Certificate: []*xtls.Certificate{xtls.ParseCertificate(ct)}, PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{"h2"}}
	ln := rxListen(t, rxIntegrationConfig(true), security, func(c stat.Connection) { c.Close() })
	proxy := newRXTCPFaultProxy(t, ln.Addr().String(), "freeze", 32)
	host, port, _ := stdnet.SplitHostPort(proxy.listener.Addr().String())
	p, _ := net.PortFromString(port)
	cc := rxIntegrationConfig(true)
	cc.Rx.RequestTimeoutMs = 100
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	c, err := Dial(ctx, net.TCPDestination(net.ParseAddress(host), p), rxStreamConfig(cc, security))
	if c != nil {
		c.Close()
	}
	if err == nil {
		t.Fatal("TLS cannot succeed with a 32-byte downstream budget")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("handshake timeout exceeded bound: %s", time.Since(start))
	}
	if proxy.cuts.Load() == 0 {
		t.Fatal("handshake fault not injected")
	}
}

func TestRXIntegrationResourceScaling(t *testing.T) {
	if os.Getenv("XRAY_RX_LONG_TEST") != "1" {
		t.Skip("set XRAY_RX_LONG_TEST=1 for process resource comparisons")
	}
	for _, count := range []int{1, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			runtime.GC()
			finishMemory := rxMemorySample()
			t.Cleanup(func() { finishMemory() })
			const perSession = 2 << 20
			ln := rxListen(t, rxIntegrationConfig(true), nil, func(c stat.Connection) {
				defer c.Close()
				var b [1]byte
				if _, err := io.ReadFull(c, b[:]); err == nil {
					rxWritePayload(c, perSession)
				}
			})
			proxy := newRXTCPFaultProxy(t, ln.Addr().String(), "fin", 16<<10)
			start := time.Now()
			t.Run("downloads", func(t *testing.T) {
				for i := range count {
					t.Run(fmt.Sprint(i), func(t *testing.T) {
						t.Parallel()
						cc := rxIntegrationConfig(true)
						cc.Rx.MaxReconnects = 1024
						c := rxDial(t, proxy.listener.Addr().String(), cc, nil)
						if _, err := c.Write([]byte{1}); err != nil {
							t.Fatal(err)
						}
						rxReadHash(t, c, perSession)
					})
				}
			})
			elapsed := time.Since(start)
			mem := finishMemory()
			rxSaveMetrics(t, map[string]any{"sessions": count, "bytes_per_session": perSession, "elapsed_seconds": elapsed.Seconds(), "tcp_connections": proxy.accepted.Load(), "cuts": proxy.cuts.Load(), "go_memory": mem, "replay_limit_per_session_bytes": 262144, "integrity_pass": !t.Failed()})
			t.Logf("LOCAL_RESOURCE sessions=%d total_bytes=%d elapsed=%s tcp_connections=%d cuts=%d go_memory=%v", count, count*perSession, elapsed, proxy.accepted.Load(), proxy.cuts.Load(), mem)
		})
	}
}
