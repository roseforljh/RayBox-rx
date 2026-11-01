package xray

import (
	"context"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
)

// Like route.trackedConn, this preserves the connection while hiding UDP methods.
type protectedUDPConn struct{ net.Conn }
type connectedUDPDialer struct{ conn net.Conn }

func (d *connectedUDPDialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, dest.String())
	if err != nil {
		return nil, err
	}
	d.conn = &protectedUDPConn{conn}
	return d.conn, nil
}

func (d *connectedUDPDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	panic("must preserve the protected connected dialer")
}

func TestSystemDialerWrapsProtectedUDPForQUIC(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		buffer := make([]byte, 1500)
		for {
			n, from, err := server.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = server.WriteTo(buffer[:n], from)
		}
	}()
	instance, protected := &core.Instance{}, &connectedUDPDialer{}
	instanceDialers.Store(instance, protected)
	defer instanceDialers.Delete(instance)
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	address := server.LocalAddr().(*net.UDPAddr)
	conn, err := (systemDialer{}).Dial(ctx, nil, xnet.UDPDestination(xnet.IPAddress(address.IP), xnet.Port(address.Port)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	packet, ok := conn.(*xnet.PacketConnWrapper)
	if !ok {
		t.Fatalf("QUIC requires PacketConnWrapper, got %T", conn)
	}
	_ = packet.SetDeadline(time.Now().Add(time.Second))
	for _, payload := range []string{"first datagram", "second"} {
		if _, err := packet.PacketConn.WriteTo([]byte(payload), packet.RemoteAddr()); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1500)
		n, from, err := packet.PacketConn.ReadFrom(buffer)
		if err != nil || string(buffer[:n]) != payload || from.String() != address.String() {
			t.Fatalf("datagram changed: %q from %v: %v", buffer[:n], from, err)
		}
	}
	_ = packet.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := packet.Read(make([]byte, 1)); err == nil {
		t.Fatal("protected UDP read deadline was lost")
	}
	_ = packet.Close()
	if _, err := protected.conn.Write([]byte("closed")); err == nil {
		t.Fatal("closing packet wrapper did not close protected connection")
	}
}
