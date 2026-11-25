package splithttp

import (
	"bytes"
	"context"
	gotls "crypto/tls"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	xnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol/tls/cert"
	"github.com/roseforljh/RayBox-rx/internal/protocol/testing/servers/tcp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
)

var idleTestPadding = "?x_padding=" + strings.Repeat("X", 200)

func setIdleTimeouts(t *testing.T, idle, ping time.Duration) {
	oldIdle, oldPing := serverIdleTimeout, serverSendPingTimeout
	serverIdleTimeout, serverSendPingTimeout = idle, ping
	t.Cleanup(func() { serverIdleTimeout, serverSendPingTimeout = oldIdle, oldPing })
}

func startIdleTestServer(t *testing.T, addConn internet.ConnHandler) (*Listener, string) {
	t.Helper()
	port := tcp.PickPort()
	ct, _ := cert.MustGenerate(nil, cert.CommonName("localhost"))
	ln, err := ListenXH(context.Background(), xnet.LocalHostIP, port, &internet.MemoryStreamConfig{
		ProtocolName:     "splithttp",
		ProtocolSettings: &Config{Path: "/sh", Mode: "packet-up", H2Params: []byte(`{"PingTimeout":300000000}`)},
		SecurityType:     "tls",
		SecuritySettings: &tls.Config{Certificate: []*tls.Certificate{tls.ParseCertificate(ct)}},
	}, addConn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.(*Listener), "127.0.0.1:" + port.String()
}

// peerConn is the client side TCP conn. closed fires once the connection is gone;
// freeze makes the peer silent (reads hang, writes are dropped) like a vanished host.
type peerConn struct {
	net.Conn
	closeOnce sync.Once
	closed    chan struct{}
	mu        sync.Mutex
	frozen    bool
	unfreeze  chan struct{}
}

func (c *peerConn) isFrozen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frozen
}

func (c *peerConn) freeze() {
	c.mu.Lock()
	c.frozen = true
	c.mu.Unlock()
}

func (c *peerConn) Read(b []byte) (int, error) {
	if c.isFrozen() {
		<-c.unfreeze
		return 0, io.ErrClosedPipe
	}
	n, err := c.Conn.Read(b)
	if err != nil {
		c.closeOnce.Do(func() { close(c.closed) })
	}
	return n, err
}

func (c *peerConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *peerConn) Write(b []byte) (int, error) {
	if c.isFrozen() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

// newPeerClient returns an HTTP/2 client that never closes idle connections itself.
// A nil t registers no cleanup, so the test itself retains nothing.
func newPeerClient(t *testing.T, addr string) (*http.Client, chan *peerConn) {
	conns := make(chan *peerConn, 16)
	tr := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			pc := &peerConn{Conn: raw, closed: make(chan struct{}), unfreeze: make(chan struct{})}
			if t != nil {
				t.Cleanup(func() { close(pc.unfreeze); raw.Close() })
			}
			tc := gotls.Client(pc, &gotls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
			if err := tc.HandshakeContext(ctx); err != nil {
				return nil, err
			}
			conns <- pc
			return tc, nil
		},
	}
	if t != nil {
		t.Cleanup(tr.CloseIdleConnections)
	}
	return &http.Client{Transport: tr}, conns
}

func getH2(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 2 {
		t.Fatalf("want HTTP/2, got %s", resp.Proto)
	}
	return resp
}

func waitClosed(t *testing.T, pc *peerConn, d time.Duration, what string) {
	t.Helper()
	select {
	case <-pc.closed:
	case <-time.After(d):
		t.Fatal(what)
	}
}

// Raw H2 client that ignores GOAWAY: the server itself must close the TCP connection.
func TestServerClosesIdleH2Conn(t *testing.T) {
	setIdleTimeouts(t, 500*time.Millisecond, 0)
	_, addr := startIdleTestServer(t, func(c stat.Connection) { c.Close() })

	c, err := gotls.Dial("tcp", addr, &gotls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if p := c.ConnectionState().NegotiatedProtocol; p != "h2" {
		t.Fatalf("ALPN %q", p)
	}
	io.WriteString(c, http2.ClientPreface)
	fr := http2.NewFramer(c, c)
	fr.WriteSettings()
	var hb bytes.Buffer
	enc := hpack.NewEncoder(&hb)
	for _, f := range [][2]string{{":method", "GET"}, {":scheme", "https"}, {":authority", "localhost"}, {":path", "/nope"}} {
		enc.WriteField(hpack.HeaderField{Name: f[0], Value: f[1]})
	}
	fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: hb.Bytes(), EndStream: true, EndHeaders: true})

	start := time.Now()
	c.SetReadDeadline(start.Add(3 * time.Second))
	sawGoAway := false
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			if err == io.EOF && sawGoAway {
				t.Logf("server closed idle connection %v after the request", time.Since(start).Round(time.Millisecond))
				return
			}
			t.Fatalf("server kept an idle HTTP/2 connection open (goaway=%v): %v", sawGoAway, err)
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				fr.WriteSettingsAck()
			}
		case *http2.GoAwayFrame:
			sawGoAway = true
		}
	}
}

func TestIdleConnBatchesReleaseResources(t *testing.T) {
	setIdleTimeouts(t, 300*time.Millisecond, 0)
	_, addr := startIdleTestServer(t, func(c stat.Connection) { c.Close() })

	runtime.GC()
	base := runtime.NumGoroutine()
	var heaps []uint64
	for batch := 0; batch < 5; batch++ {
		const n = 200
		pcs := make(chan *peerConn, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				client, conns := newPeerClient(nil, addr)
				resp, err := client.Get("https://localhost/nope")
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				pcs <- <-conns
			}()
		}
		wg.Wait()
		close(pcs)
		if t.Failed() {
			return
		}
		peak := runtime.NumGoroutine()
		for pc := range pcs {
			waitClosed(t, pc, 5*time.Second, "idle connection was not reclaimed")
		}
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > base+10 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if g := runtime.NumGoroutine(); g > base+10 {
			t.Fatalf("batch %d: goroutines %d, baseline %d", batch, g, base)
		}
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		heaps = append(heaps, ms.HeapInuse)
		t.Logf("batch %d: peak goroutines %d, after reclaim %d, heap in use %d KiB", batch, peak, runtime.NumGoroutine(), ms.HeapInuse>>10)
	}
	if heaps[4] > heaps[1]+2<<20 { // batch 0 warms up pools
		t.Fatalf("heap keeps growing across batches: %v", heaps)
	}
}

func TestH2PingClosesSilentPeerWithOpenGET(t *testing.T) {
	setIdleTimeouts(t, time.Hour, 300*time.Millisecond)
	released := make(chan struct{})
	l, addr := startIdleTestServer(t, func(c stat.Connection) {
		go func() {
			var b [1]byte
			c.Read(b[:]) // returns once the GET handler has closed the conn
			close(released)
		}()
	})
	client, conns := newPeerClient(t, addr)

	resp := getH2(t, client, "https://localhost/sh/silent"+idleTestPadding)
	defer resp.Body.Close()
	(<-conns).freeze()

	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("GET to a silent peer was never released")
	}
	waitSessionGone(t, l, "silent")
}

func waitSessionGone(t *testing.T, l *Listener, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := l.rxHandler.sessions.Load(id); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session still registered")
}

func TestCloseUnblocksStalledDownlinkWrite(t *testing.T) {
	setIdleTimeouts(t, time.Hour, time.Hour)
	opened := make(chan stat.Connection, 1)
	writerDone := make(chan struct{})
	l, addr := startIdleTestServer(t, func(c stat.Connection) {
		go func() {
			defer close(writerDone)
			b := make([]byte, 64<<10)
			for {
				if _, err := c.Write(b); err != nil {
					return
				}
			}
		}()
		opened <- c
	})
	client, _ := newPeerClient(t, addr)

	resp := getH2(t, client, "https://localhost/sh/stalled"+idleTestPadding)
	defer resp.Body.Close() // never read: the peer stops granting flow-control window
	c := <-opened
	time.Sleep(500 * time.Millisecond)

	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	for _, ch := range []chan struct{}{closed, writerDone} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("Close stayed blocked behind a stalled downlink Write")
		}
	}
	waitSessionGone(t, l, "stalled")
}

func TestActiveSilentGETSurvivesIdleAndPing(t *testing.T) {
	setIdleTimeouts(t, 300*time.Millisecond, 300*time.Millisecond)
	_, addr := startIdleTestServer(t, func(c stat.Connection) {
		go func() {
			defer c.Close()
			time.Sleep(2 * time.Second) // no data well past idle and ping timeouts
			c.Write([]byte("hello"))
		}()
	})
	client, _ := newPeerClient(t, addr)

	resp := getH2(t, client, "https://localhost/sh/active"+idleTestPadding)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "hello" {
		t.Fatalf("live XHTTP stream was cut: %q, %v", body, err)
	}
}
