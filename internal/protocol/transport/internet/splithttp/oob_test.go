package splithttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol/tls/cert"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/serial"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	xtls "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
)

func TestOOBUrgentByteIsExcludedFromTLSStream(t *testing.T) {
	for _, position := range []int{0, 1} {
		t.Run(strconv.Itoa(position), func(t *testing.T) {
			ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			client, err := stdnet.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client.SetDeadline(time.Now().Add(time.Second))
			server.SetDeadline(time.Now().Add(time.Second))
			conn := &oobConn{Conn: client, ctx: context.Background(), position: position, delay: time.Millisecond}
			hello := []byte{22, 3, 1, 0, 4, 1, 2, 3, 4}
			if n, err := conn.Write(hello); n != len(hello) || err != nil {
				t.Fatal(n, err)
			}
			if _, err := conn.Write([]byte("later")); err != nil {
				t.Fatal(err)
			}
			want := append(append([]byte(nil), hello...), []byte("later")...)
			got := make([]byte, len(want))
			if _, err := io.ReadFull(server, got); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("got %x want %x err=%v", got, want, err)
			}
		})
	}
}

func oobDestination(t *testing.T, address string) net.Destination {
	t.Helper()
	host, port, err := stdnet.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	p, err := net.PortFromString(port)
	if err != nil {
		t.Fatal(err)
	}
	return net.TCPDestination(net.ParseAddress(host), p)
}

func oobTestMSS() int32 {
	if runtime.GOOS == "windows" {
		return -1
	} // Winsock TCP_MAXSEG is read-only.
	return 1440
}

func TestOOBPrimaryVerifiedTLS(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
			serverTLS := &xtls.Config{Certificate: []*xtls.Certificate{xtls.ParseCertificate(ct)}, NextProtocol: []string{"h2", "http/1.1"}}
			ln := rxListen(t, rxIntegrationConfig(false), serverTLS, func(c stat.Connection) { defer c.Close(); io.Copy(c, c) })
			c := rxIntegrationConfig(false)
			c.Oob = &OOBConfig{Enabled: true, AttemptTimeoutMs: 1000, TcpMaxSeg: oobTestMSS(), HttpVersions: []string{protocol}}
			security := &xtls.Config{ServerName: "localhost", PinnedPeerCertSha256: [][]byte{hash[:]}, Fingerprint: "chrome", NextProtocol: []string{protocol}}
			stream := rxStreamConfig(c, security)
			dest := oobDestination(t, ln.Addr().String())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := Dial(ctx, dest, stream)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			observed := conn.(*oobObservedConn)
			if observed.policy.preferred != -1 {
				t.Fatal("HTTP headers prematurely verified a strategy")
			}
			payload := bytes.Repeat([]byte("native-oob-stream;"), 4096)
			if _, err := conn.Write(payload); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
				t.Fatal("payload corruption or failure", err)
			}
			if observed.policy.preferred != observed.index {
				t.Fatal("actual data delivery was not remembered")
			}
		})
	}
}

func TestOOBRetriesHTTPStartupAndTimesOutHeaders(t *testing.T) {
	for _, failure := range []string{"503", "freeze"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			payload := bytes.Repeat([]byte("checked-data;"), 4096)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					if failure == "freeze" {
						<-r.Context().Done()
					} else {
						w.WriteHeader(503)
					}
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				w.Write(payload)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			srv.StartTLS()
			defer srv.Close()
			c := rxIntegrationConfig(false)
			c.Oob = &OOBConfig{Enabled: true, MaxAttempts: 2, AttemptTimeoutMs: 300, TcpMaxSeg: oobTestMSS(), HttpVersions: []string{"http/1.1"}}
			hash := sha256.Sum256(srv.Certificate().Raw)
			security := &xtls.Config{ServerName: "example.com", PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{"http/1.1"}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := Dial(ctx, oobDestination(t, srv.Listener.Addr().String()), rxStreamConfig(c, security))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			got, err := io.ReadAll(conn)
			if err != nil || !bytes.Equal(got, payload) || requests.Load() != 2 {
				t.Fatal(len(got), err, requests.Load())
			}
		})
	}
}

func TestOOBFallbackBeforeApplicationUpload(t *testing.T) {
	blackhole, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	var calls atomic.Int32
	go func() {
		for {
			c, err := blackhole.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			c.Close()
		}
	}()
	serverConfig := rxIntegrationConfig(true)
	ln := rxListen(t, serverConfig, nil, func(c stat.Connection) { defer c.Close(); io.Copy(c, c) })
	fallbackDest := oobDestination(t, ln.Addr().String())
	fallback := &internet.StreamConfig{ProtocolName: "splithttp", Address: net.NewIPOrDomain(fallbackDest.Address), Port: uint32(fallbackDest.Port), TransportSettings: []*internet.TransportConfig{{ProtocolName: "splithttp", Settings: serial.ToTypedMessage(serverConfig)}}}
	c := rxIntegrationConfig(false)
	c.Oob = &OOBConfig{Enabled: true, MaxAttempts: 2, AttemptTimeoutMs: 300, TcpMaxSeg: oobTestMSS(), FallbackSettings: fallback}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, err := Dial(ctx, oobDestination(t, blackhole.Addr().String()), rxStreamConfig(c, &xtls.Config{ServerName: "localhost", NextProtocol: []string{"h2"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := bytes.Repeat([]byte("RX-fallback-data;"), 2048)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("fallback data", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatal("unexpected primary attempt count", n)
	}
}

func TestOOBPolicyCoolingRecoveryAndPreference(t *testing.T) {
	p := &oobPolicy{preferred: -1, candidates: make([]oobCandidate, 2)}
	p.report(0, true)
	if p.pick(nil) != 0 {
		t.Fatal("verified strategy not preferred")
	}
	p.report(0, false)
	if p.pick(nil) != 1 {
		t.Fatal("failed strategy was not cooled")
	}
	p.report(1, false)
	if p.pick(nil) != -1 {
		t.Fatal("all-cold pool should stop new primary attempts")
	}
	p.candidates[0].coolUntil = time.Time{}
	if p.pick(nil) != 0 {
		t.Fatal("expired candidate not retried")
	}
	if p.pick(map[int]bool{0: true}) != -1 {
		t.Fatal("same candidate retried within startup budget")
	}
}

func TestOOBStartupCancellationDoesNotRetry(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); <-r.Context().Done() }))
	srv.StartTLS()
	defer srv.Close()
	hash := sha256.Sum256(srv.Certificate().Raw)
	c := rxIntegrationConfig(false)
	c.Oob = &OOBConfig{Enabled: true, MaxAttempts: 4, TcpMaxSeg: oobTestMSS(), HttpVersions: []string{"http/1.1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Dial(ctx, oobDestination(t, srv.Listener.Addr().String()), rxStreamConfig(c, &xtls.Config{ServerName: "example.com", PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{"http/1.1"}}))
	if err != context.DeadlineExceeded || time.Since(started) > time.Second || requests.Load() != 1 {
		t.Fatal(err, time.Since(started), requests.Load())
	}
}
