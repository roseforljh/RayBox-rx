package splithttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol/tls/cert"
	"github.com/roseforljh/RayBox-rx/internal/protocol/testing/servers/tcp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
)

// Compare only concurrency; keep certificate, server, payloads and rotation
// policy identical. This is a loopback resource/integrity check, not a Russia
// network benchmark and not a justification to change the global default.
func TestXMUXConcurrencyProfilesTLS(t *testing.T) {
	ct, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
	port := tcp.PickPort()
	serverSettings := &internet.MemoryStreamConfig{
		ProtocolName: "splithttp", ProtocolSettings: &Config{Path: "/xmux-profiles", Mode: "stream-up"},
		SecurityType: "tls", SecuritySettings: &tls.Config{
			Certificate: []*tls.Certificate{tls.ParseCertificate(ct)}, NextProtocol: []string{"h2"},
		},
	}
	listener, err := ListenXH(context.Background(), net.LocalHostIP, port, serverSettings, func(conn stat.Connection) {
		go func() {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, concurrency := range []int32{1, 5} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			settings := &internet.MemoryStreamConfig{
				ProtocolName: "splithttp", ProtocolSettings: &Config{
					Path: "/xmux-profiles", Mode: "stream-up", Xmux: &XmuxConfig{
						MaxConcurrency:   &RangeConfig{From: concurrency, To: concurrency},
						HMaxRequestTimes: &RangeConfig{From: 600, To: 600},
						HMaxReusableSecs: &RangeConfig{From: 1800, To: 1800},
					},
				},
				SecurityType: "tls", SecuritySettings: &tls.Config{
					ServerName: "localhost", PinnedPeerCertSha256: [][]byte{hash[:]}, NextProtocol: []string{"h2"},
				},
			}
			defer CloseStream(settings)
			destination := net.TCPDestination(net.DomainAddress("localhost"), port)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			const sessions = 20
			start := time.Now()
			connections := make([]stat.Connection, 0, sessions)
			localAddresses := make(map[string]bool)
			for range sessions {
				conn, dialErr := Dial(ctx, destination, settings)
				if dialErr != nil {
					t.Fatal(dialErr)
				}
				defer conn.Close()
				connections = append(connections, conn)
				localAddresses[conn.LocalAddr().String()] = true
			}
			globalDialerAccess.Lock()
			manager := globalDialerMap[dialerConf{destination, settings}]
			clients := append([]*XmuxClient(nil), manager.xmuxClients...)
			globalDialerAccess.Unlock()
			wantClients := (sessions + int(concurrency) - 1) / int(concurrency)
			if len(clients) != wantClients || len(localAddresses) != wantClients {
				t.Fatalf("transports=%d sockets=%d, want %d", len(clients), len(localAddresses), wantClients)
			}
			var wg sync.WaitGroup
			errs := make(chan error, sessions)
			firstBytes := make(chan time.Duration, sessions)
			for i, conn := range connections {
				wg.Add(1)
				go func() {
					defer wg.Done()
					payload := bytes.Repeat([]byte{byte(i + 1)}, 16*1024)
					requestStart := time.Now()
					if _, err := conn.Write(payload); err != nil {
						errs <- err
						return
					}
					response := make([]byte, len(payload))
					if _, err := io.ReadFull(conn, response[:1]); err != nil {
						errs <- err
						return
					}
					firstBytes <- time.Since(requestStart)
					if _, err := io.ReadFull(conn, response[1:]); err != nil {
						errs <- err
					} else if !bytes.Equal(payload, response) {
						errs <- fmt.Errorf("session %d data changed", i)
					}
				}()
			}
			wg.Wait()
			close(errs)
			failures := 0
			for err := range errs {
				failures++
				t.Error(err)
			}
			close(firstBytes)
			var firstByteTotal time.Duration
			for duration := range firstBytes {
				firstByteTotal += duration
			}
			t.Logf("concurrency=%d replies=%d/%d transports=%d sockets=%d bytes=%d elapsed=%s meanTTFB=%s", concurrency, sessions-failures, sessions, len(clients), len(localAddresses), sessions*16*1024, time.Since(start), firstByteTotal/sessions)
			for _, conn := range connections {
				conn.Close()
			}
			// Exercise request-count rotation without a 30-minute sleep.
			for _, client := range clients {
				client.LeftRequests.Store(0)
			}
			replacement, err := Dial(ctx, destination, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Close()
			for _, client := range clients {
				if !client.NotUsed.Load() || !client.XmuxConn.IsClosed() {
					t.Fatal("expired idle transport retained after replacement")
				}
			}
			globalDialerAccess.Lock()
			timed := manager.xmuxClients[0]
			timed.UnreusableAt = time.Now().Add(-time.Second)
			globalDialerAccess.Unlock()
			rotated, err := Dial(ctx, destination, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer rotated.Close()
			if !timed.NotUsed.Load() || timed.XmuxConn.IsClosed() {
				t.Fatal("time-expired active transport must retire without closing its session")
			}
			replacement.Close()
			if !timed.XmuxConn.IsClosed() {
				t.Fatal("retired transport remained open after its last session closed")
			}
		})
	}
}
