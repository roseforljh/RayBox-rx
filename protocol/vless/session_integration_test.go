package vless

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/adapter"
	outboundadapter "github.com/roseforljh/RayBox-rx/adapter/outbound"
	xnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/infra/conf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/splithttp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	vmessvless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const sessionTestUUID = "11111111-1111-4111-8111-111111111111"

type sessionTestDetour struct {
	outboundadapter.Adapter
	calls  atomic.Int32
	active atomic.Int32
	port   uint16
}

func (d *sessionTestDetour) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if network != "tcp" || dest.String() != fmt.Sprintf("127.0.0.1:%d", d.port) {
		return nil, fmt.Errorf("unexpected physical destination: %s %s", network, dest)
	}
	d.calls.Add(1)
	conn, err := (&net.Dialer{}).DialContext(ctx, network, dest.String())
	if err != nil {
		return nil, err
	}
	d.active.Add(1)
	return &sessionTrackedConn{Conn: conn, active: &d.active}, nil
}

type sessionTrackedConn struct {
	net.Conn
	active *atomic.Int32
	once   sync.Once
}

func (c *sessionTrackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.active.Add(-1) })
	return err
}

func (*sessionTestDetour) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("unexpected physical UDP listener")
}

type sessionTestManager struct {
	adapter.OutboundManager
	detour *sessionTestDetour
}

func (m sessionTestManager) Outbound(tag string) (adapter.Outbound, bool) {
	return m.detour, tag == "protected"
}

func startSessionTestServer(t *testing.T, ctx context.Context) (uint16, string) {
	t.Helper()
	fixture := httptest.NewTLSServer(nil)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.Certificate().Raw})
	keyDER, err := x509.MarshalPKCS8PrivateKey(fixture.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	fixture.Close()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(reserved.Addr().(*net.TCPAddr).Port)
	reserved.Close()
	// Use only transport and protocol handlers: no second kernel instance on either end.
	raw, err := json.Marshal(map[string]any{
		"network": "xhttp", "security": "tls",
		"xhttpSettings": map[string]any{"path": "/raybox-session", "host": "example.com", "mode": "auto"},
		"tlsSettings": map[string]any{"alpn": []string{"h2", "http/1.1"}, "certificates": []any{
			map[string]any{"certificate": strings.Split(string(certPEM), "\n"), "key": strings.Split(string(keyPEM), "\n")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stream conf.StreamConfig
	if err = json.Unmarshal(raw, &stream); err != nil {
		t.Fatal(err)
	}
	built, err := stream.Build()
	if err != nil {
		t.Fatal(err)
	}
	memory, err := internet.ToMemoryStreamConfig(built)
	if err != nil {
		t.Fatal(err)
	}
	handler := adapter.NewUpstreamContextHandler(
		func(_ context.Context, conn net.Conn, _ adapter.InboundContext, _ N.CloseHandlerFunc) {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		},
		func(_ context.Context, conn N.PacketConn, _ adapter.InboundContext, _ N.CloseHandlerFunc) {
			defer conn.Close()
			for {
				buffer := buf.NewPacket()
				buffer.Resize(N.CalculateFrontHeadroom(conn), 0)
				destination, err := conn.ReadPacket(buffer)
				if err != nil {
					buffer.Release()
					return
				}
				if err = conn.WritePacket(buffer, destination); err != nil {
					return
				}
			}
		},
	)
	protocol := vmessvless.NewService[int](log.NewNOPFactory().Logger(), handler)
	protocol.UpdateUsers([]int{1}, []string{sessionTestUUID}, []string{""})
	listener, err := splithttp.ListenXH(ctx, xnet.LocalHostIP, xnet.Port(port), memory, func(conn stat.Connection) {
		go func() {
			if err := protocol.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), nil); err != nil {
				conn.Close()
			}
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return port, string(certPEM)
}

func TestStandardVLESSXHTTPVerifiedTLSAndReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	port, certificate := startSessionTestServer(t, ctx)
	protected := &sessionTestDetour{
		Adapter: outboundadapter.NewAdapter("test", "protected", []string{"tcp"}, nil), port: port,
	}
	ctx = service.ContextWith[adapter.OutboundManager](ctx, sessionTestManager{detour: protected})
	logger := log.NewNOPFactory().Logger()
	options := option.VLESSOutboundOptions{
		DialerOptions: option.DialerOptions{Detour: "protected"},
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: port},
		UUID:          sessionTestUUID,
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{
			Enabled: true, ServerName: "example.com", Certificate: badoption.Listable[string]{certificate},
		}},
		Transport: &option.V2RayTransportOptions{Type: "xhttp", XHTTPOptions: option.XHTTPOptions{
			Host: "example.com", Path: "/raybox-session", Mode: "packet-up",
			Extra: json.RawMessage(`{"headers":{"X-RayBox-Test":"public-model"}}`),
		}},
	}
	xudp, disabled := "xudp", ""
	for _, encoding := range []struct {
		name  string
		value *string
	}{{"default", nil}, {"xudp", &xudp}, {"disabled", &disabled}} {
		t.Run(encoding.name, func(t *testing.T) {
			options := options
			options.PacketEncoding = encoding.value
			for reload := 0; reload < 2; reload++ {
				client, err := NewOutbound(ctx, nil, logger, "session", options)
				if err != nil {
					t.Fatal(err)
				}
				if client.Type() != "vless" || client.Tag() != "session" {
					t.Fatal("standard protocol identity changed")
				}
				scope := adapter.NewScope(ctx, logger)
				t.Cleanup(func() { scope.Close() })
				if err = client.(adapter.Lifecycle).Start(adapter.StartStateStart, scope); err != nil {
					t.Fatal(err)
				}
				conn, err := client.DialContext(ctx, "tcp", M.ParseSocksaddr("example.org:443"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Close() })
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				message := fmt.Sprintf("RayBox standard VLESS XHTTP reload %d", reload)
				if _, err = io.WriteString(conn, message); err != nil {
					t.Fatal(err)
				}
				buffer := make([]byte, len(message))
				if _, err = io.ReadFull(conn, buffer); err != nil {
					t.Fatal(err)
				}
				if string(buffer) != message {
					t.Fatal("standard XHTTP payload corrupted")
				}
				packets, err := client.ListenPacket(ctx, M.ParseSocksaddr("127.0.0.2:8000"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { packets.Close() })
				for index, target := range []string{"127.0.0.2:8000", "127.0.0.3:53", "127.0.0.4:443"} {
					payload := strings.Repeat(fmt.Sprintf("packet-%d;", index), 1+index*300)
					destination := M.ParseSocksaddr(target)
					packetConn := packets
					if encoding.value != nil && *encoding.value == "" {
						packetConn, err = client.ListenPacket(ctx, destination)
						if err != nil {
							t.Fatal(err)
						}
					}
					packetConn.SetReadDeadline(time.Now().Add(5 * time.Second))
					if _, err = packetConn.WriteTo([]byte(payload), destination); err != nil {
						t.Fatal(err)
					}
					packet := make([]byte, 8192)
					n, source, err := packetConn.ReadFrom(packet)
					if packetConn != packets {
						packetConn.Close()
					}
					if err != nil {
						t.Fatal(err)
					}
					if string(packet[:n]) != payload || source.String() != target {
						t.Fatalf("UDP boundary or destination changed: %d %v", n, source)
					}
				}
				if encoding.value != nil && *encoding.value == "" {
					if _, err = packets.WriteTo([]byte("wrong-target"), M.ParseSocksaddr("127.0.0.9:9000")); err == nil {
						t.Fatal("fixed-target UDP silently accepted a different destination")
					}
				}
				packets.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
				if _, _, err = packets.ReadFrom(make([]byte, 1)); err == nil {
					t.Fatal("UDP read deadline ignored")
				} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
					t.Fatalf("UDP deadline returned a non-timeout error: %v", err)
				}
				packets.Close()
				if err = scope.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = conn.Read(buffer); err == nil {
					t.Fatal("scope close left session readable")
				}
				if active := protected.active.Load(); active != 0 {
					t.Fatalf("scope close leaked %d protected physical connections", active)
				}
				if _, err = client.DialContext(ctx, "tcp", M.ParseSocksaddr("example.org:443")); err == nil {
					t.Fatal("closed scope accepted a new session")
				}
			}
		})
	}
	if protected.calls.Load() < 6 {
		t.Fatalf("reload bypassed protected detour: %d physical dials", protected.calls.Load())
	}
	for _, invalid := range []string{"wrong-name", "untrusted-certificate"} {
		t.Run(invalid, func(t *testing.T) {
			badOptions := options
			badTLS := *options.TLS
			badOptions.TLS = &badTLS
			if invalid == "wrong-name" {
				badTLS.ServerName = "wrong.example.org"
			} else {
				badTLS.Certificate = nil
			}
			client, err := NewOutbound(ctx, nil, logger, "invalid-tls", badOptions)
			if err != nil {
				t.Fatal(err)
			}
			scope := adapter.NewScope(ctx, logger)
			defer scope.Close()
			if err = client.(adapter.Lifecycle).Start(adapter.StartStateStart, scope); err != nil {
				t.Fatal(err)
			}
			conn, err := client.DialContext(ctx, "tcp", M.ParseSocksaddr("example.org:443"))
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = io.WriteString(conn, "must not echo"); err != nil {
				return
			}
			if _, err = conn.Read(make([]byte, 12)); err == nil {
				t.Fatal("invalid TLS identity or trust was accepted")
			}
		})
	}
}
