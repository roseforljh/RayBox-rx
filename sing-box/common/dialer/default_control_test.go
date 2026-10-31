package dialer

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestTCPTransportControlBeforeConnect(t *testing.T) {
	var calls atomic.Int32
	options := option.DialerOptions{}
	options.TCPControl = func(network, address string, raw syscall.RawConn) error {
		calls.Add(1)
		return raw.Control(func(fd uintptr) {
			if fd == 0 {
				t.Error("missing native socket")
			}
		})
	}
	dialer, err := NewDefault(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := dialer.DialContext(context.Background(), "tcp", M.SocksaddrFromNet(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	peer.Close()
	if calls.Load() != 1 {
		t.Fatal("TCP transport control was not applied", calls.Load())
	}
	udp, err := dialer.DialContext(context.Background(), "udp", M.ParseSocksaddr("127.0.0.1:9"))
	if err != nil {
		t.Fatal(err)
	}
	udp.Close()
	if calls.Load() != 1 {
		t.Fatal("TCP control affected UDP", calls.Load())
	}
}
