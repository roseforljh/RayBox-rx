package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/vless/inbound"
)

const testUUID = "11111111-1111-4111-8111-111111111111"

// Xray blocks private destinations behind VLESS inbounds by default; the test servers echo on loopback.
var loopbackFreedom = map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": []string{"tcp", "udp"}, "ip": []string{"127.0.0.1/32"}}}}}

type testDialer struct{ calls atomic.Int32 }

func (d *testDialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	d.calls.Add(1)
	if network == "udp" {
		conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		return &xnet.PacketConnWrapper{PacketConn: conn, Dest: dest.UDPAddr()}, nil
	}
	return (&net.Dialer{}).DialContext(ctx, network, dest.String())
}
func (d *testDialer) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
}

func startProxy(t *testing.T) (int, *core.Instance) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	var config conf.Config
	raw := []byte(`{"log":{"loglevel":"none"},"inbounds":[{"listen":"127.0.0.1","port":` + fmtInt(port) + `,"protocol":"vless","settings":{"clients":[{"id":"` + testUUID + `"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","network":["tcp","udp"],"ip":["127.0.0.1/32"]}]}}]}`)
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	built, err := config.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(built)
	if err != nil {
		t.Fatal(err)
	}
	instanceDialers.Store(instance, &testDialer{})
	t.Cleanup(func() { instance.Close(); instanceDialers.Delete(instance) })
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	return port, instance
}

func fmtInt(value int) string { raw, _ := json.Marshal(value); return string(raw) }

func startClient(t *testing.T, port int) (*Outbound, *testDialer, *adapter.Scope) {
	t.Helper()
	config, err := buildConfig(json.RawMessage(`{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":` + fmtInt(port) + `,"users":[{"id":"` + testUUID + `","encryption":"none"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	dialer := &testDialer{}
	out := &Outbound{ctx: context.Background(), config: config, dialer: dialer}
	scope := adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())
	t.Cleanup(func() { scope.Close() })
	if err := out.Start(adapter.StartStateStart, scope); err != nil {
		t.Fatal(err)
	}
	return out, dialer, scope
}

func TestTCPAndReloadIsolation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	port, _ := startProxy(t)
	old, oldDialer, oldScope := startClient(t, port)
	next, nextDialer, _ := startClient(t, port)
	check := func(out *Outbound) {
		conn, err := out.DialContext(context.Background(), "tcp", M.SocksaddrFromNet(listener.Addr()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 5)
		if _, err := io.ReadFull(conn, buffer); err != nil {
			t.Fatal(err)
		}
		if string(buffer) != "hello" {
			t.Fatal(string(buffer))
		}
	}
	check(old)
	check(next)
	oldScope.Close()
	if _, ok := instanceDialers.Load(old.instance); ok {
		t.Fatal("old dialer leaked")
	}
	check(next)
	if oldDialer.calls.Load() == 0 || nextDialer.calls.Load() < 2 {
		t.Fatal("Xray bypassed protected per-instance dialers")
	}
}

func TestUDPBoundariesDestinationsAndDeadline(t *testing.T) {
	var servers []net.PacketConn
	for range 2 {
		server, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		servers = append(servers, server)
		t.Cleanup(func() { server.Close() })
		go func() {
			buffer := make([]byte, 65535)
			for {
				n, addr, err := server.ReadFrom(buffer)
				if err != nil {
					return
				}
				server.WriteTo(buffer[:n], addr)
			}
		}()
	}
	port, _ := startProxy(t)
	client, protected, _ := startClient(t, port)
	conn, err := client.ListenPacket(context.Background(), M.SocksaddrFromNet(servers[0].LocalAddr()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, size := range []int{64, 4000, 128} {
		server := servers[index%2]
		payload := bytes.Repeat([]byte{byte(index + 1)}, size)
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if n, err := conn.WriteTo(payload, server.LocalAddr()); err != nil || n != size {
			t.Fatalf("write: %d %v", n, err)
		}
		buffer := make([]byte, 65535)
		n, addr, err := conn.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("UDP packet %d: %v", index, err)
		}
		if !bytes.Equal(payload, buffer[:n]) || addr.String() != server.LocalAddr().String() {
			t.Fatalf("UDP boundary/destination lost: %d %s", n, addr)
		}
	}
	if _, err := conn.WriteTo(make([]byte, 12000), servers[0].LocalAddr()); err == nil {
		t.Fatal("oversized XUDP packet must fail explicitly")
	}
	conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := conn.ReadFrom(make([]byte, 100)); err == nil {
		t.Fatal("read deadline ignored")
	}
	if protected.calls.Load() == 0 {
		t.Fatal("UDP bypassed protected dialer")
	}
}

func TestRejectUnsafeConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{"protocol":"freedom"}`,
		`{"protocol":"vless","proxySettings":{}}`,
		`{"protocol":"vless","streamSettings":{"sockopt":{"dialerProxy":"escape"}}}`,
	} {
		if _, err := buildConfig(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted unsafe config", raw)
		}
	}
	_, err := (systemDialer{}).Dial(context.Background(), nil, xnet.TCPDestination(xnet.LocalHostIP, 80), nil)
	if err == nil {
		t.Fatal("missing dialer must fail closed")
	}
}
