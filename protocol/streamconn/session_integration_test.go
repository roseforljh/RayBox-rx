package streamconn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	standardtrojan "github.com/roseforljh/RayBox-rx/protocol/trojan"
	standardvmess "github.com/roseforljh/RayBox-rx/protocol/vmess"
	"github.com/roseforljh/RayBox-rx/transport/trojan"
	"github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	xhttpUUID     = "11111111-1111-4111-8111-111111111111"
	xhttpPassword = "raybox-xhttp-test-password"
	xhttpPath     = "/raybox-vmess-trojan"
)

type shiftedClock struct{}

func (shiftedClock) TimeFunc() func() time.Time {
	return func() time.Time { return time.Now().Add(time.Hour) }
}

type xhttpDetour struct {
	outboundadapter.Adapter
	port  uint16
	calls atomic.Int32
}

func (d *xhttpDetour) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if network != "tcp" || dest.String() != fmt.Sprintf("127.0.0.1:%d", d.port) {
		return nil, fmt.Errorf("unexpected physical destination: %s %s", network, dest)
	}
	d.calls.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, dest.String())
}

func (*xhttpDetour) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("unexpected physical UDP listener")
}

type xhttpManager struct {
	adapter.OutboundManager
	detour *xhttpDetour
}

func (m xhttpManager) Outbound(tag string) (adapter.Outbound, bool) {
	return m.detour, tag == "protected"
}

func startXHTTPServer(t *testing.T, ctx context.Context, protocol string) uint16 {
	t.Helper()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(reserved.Addr().(*net.TCPAddr).Port)
	_ = reserved.Close()
	raw, err := json.Marshal(map[string]any{
		"network": "xhttp", "xhttpSettings": map[string]any{"path": xhttpPath, "host": "example.com", "mode": "auto"},
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
				packet := buf.NewPacket()
				packet.Resize(N.CalculateFrontHeadroom(conn), 0)
				destination, readErr := conn.ReadPacket(packet)
				if readErr != nil {
					packet.Release()
					return
				}
				if writeErr := conn.WritePacket(packet, destination); writeErr != nil {
					return
				}
			}
		},
	)
	logger := log.NewNOPFactory().Logger()
	var addConn internet.ConnHandler
	if protocol == "vmess" {
		svc := vmess.NewService[int](handler, vmess.ServiceWithDisableHeaderProtection())
		if err = svc.UpdateUsers([]int{1}, []string{xhttpUUID}, []int{0}); err != nil {
			t.Fatal(err)
		}
		if err = svc.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		addConn = func(conn stat.Connection) {
			go func() {
				if serviceErr := svc.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), nil); serviceErr != nil {
					t.Logf("VMess server: %v", serviceErr)
					_ = conn.Close()
				}
			}()
		}
	} else {
		service := trojan.NewService[int](handler, nil, logger)
		if err = service.UpdateUsers([]int{1}, []string{xhttpPassword}); err != nil {
			t.Fatal(err)
		}
		addConn = func(conn stat.Connection) {
			go func() {
				if serviceErr := service.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), nil); serviceErr != nil {
					_ = conn.Close()
				}
			}()
		}
	}
	listener, err := splithttp.ListenXH(ctx, xnet.LocalHostIP, xnet.Port(port), memory, addConn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return port
}

func TestStandardVMessAndTrojanXHTTPLoopback(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol string
		password string
	}{
		{name: "vmess-default", protocol: "vmess"},
		{name: "vmess-xudp", protocol: "vmess", password: "xudp"},
		{name: "trojan", protocol: "trojan", password: xhttpPassword},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			port := startXHTTPServer(t, ctx, test.protocol)
			detour := &xhttpDetour{Adapter: outboundadapter.NewAdapter("test", "protected", []string{"tcp"}, nil), port: port}
			ctx = service.ContextWith[adapter.OutboundManager](ctx, xhttpManager{detour: detour})
			logger := log.NewNOPFactory().Logger()
			transport := &option.V2RayTransportOptions{Type: "xhttp", XHTTPOptions: option.XHTTPOptions{
				Host: "example.com", Path: xhttpPath, Mode: "auto",
			}}
			var client adapter.Outbound
			var err error
			if test.protocol == "vmess" {
				client, err = standardvmess.NewOutbound(ctx, nil, logger, test.name, option.VMessOutboundOptions{
					DialerOptions: option.DialerOptions{Detour: "protected"}, ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: port},
					UUID: xhttpUUID, Transport: transport, PacketEncoding: test.password,
				})
			} else {
				client, err = standardtrojan.NewOutbound(ctx, nil, logger, test.name, option.TrojanOutboundOptions{
					DialerOptions: option.DialerOptions{Detour: "protected"}, ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: port},
					Password: xhttpPassword, Transport: transport,
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			scope := adapter.NewScope(ctx, logger)
			defer scope.Close()
			if err = client.(adapter.Lifecycle).Start(adapter.StartStateStart, scope); err != nil {
				t.Fatal(err)
			}
			conn, err := client.DialContext(ctx, "tcp", M.ParseSocksaddr("echo.example:443"))
			if err != nil {
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			message := "RayBox VMess/Trojan XHTTP TCP echo"
			if _, err = io.WriteString(conn, message); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(message))
			if _, err = io.ReadFull(conn, got); err != nil || string(got) != message {
				t.Fatalf("TCP echo mismatch: %q, %v", got, err)
			}
			defer conn.Close()
			packets, err := client.ListenPacket(ctx, M.ParseSocksaddr("127.0.0.2:8000"))
			if err != nil {
				t.Fatal(err)
			}
			targets := []string{"127.0.0.2:8000"}
			if test.name != "vmess-default" {
				targets = append(targets, "127.0.0.3:53")
			}
			for index, target := range targets {
				destination := M.ParseSocksaddr(target)
				payload := []byte(fmt.Sprintf("packet-%d-%s", index, test.name))
				if _, err = packets.WriteTo(payload, destination); err != nil {
					t.Fatal(err)
				}
				packets.SetReadDeadline(time.Now().Add(5 * time.Second))
				buffer := make([]byte, 2048)
				n, source, readErr := packets.ReadFrom(buffer)
				if readErr != nil || string(buffer[:n]) != string(payload) || source.String() != target {
					t.Fatalf("UDP echo mismatch: source=%v payload=%q err=%v", source, buffer[:n], readErr)
				}
			}
			if err = packets.Close(); err != nil {
				t.Fatal(err)
			}
			if err = scope.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = conn.Read(got); err == nil {
				t.Fatal("scope close left TCP session readable")
			}
			if detour.calls.Load() == 0 {
				t.Fatal("protected detour was not used")
			}
		})
	}
}
