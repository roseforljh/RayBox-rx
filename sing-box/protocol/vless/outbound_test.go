package vless

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	outboundadapter "github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	vmessvless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type staticVLESSDialer struct {
	conn net.Conn
}

func (d staticVLESSDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return d.conn, nil
}

func (staticVLESSDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unexpected packet listener")
}

type failingVLESSConn struct {
	writeErr error
	closed   bool
}

func (c *failingVLESSConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *failingVLESSConn) Write([]byte) (int, error)        { return 0, c.writeErr }
func (c *failingVLESSConn) Close() error                     { c.closed = true; return nil }
func (c *failingVLESSConn) LocalAddr() net.Addr              { return nil }
func (c *failingVLESSConn) RemoteAddr() net.Addr             { return nil }
func (c *failingVLESSConn) SetDeadline(time.Time) error      { return nil }
func (c *failingVLESSConn) SetReadDeadline(time.Time) error  { return nil }
func (c *failingVLESSConn) SetWriteDeadline(time.Time) error { return nil }

func newXUDPTestDialer(t *testing.T, conn net.Conn) *vlessDialer {
	t.Helper()
	client, err := vmessvless.NewClient("00000000-0000-0000-0000-000000000001", "", logger.NOP())
	if err != nil {
		t.Fatalf("create vless client: %v", err)
	}
	return &vlessDialer{
		Adapter: outboundadapter.NewAdapter(C.TypeVLESS, "test", []string{N.NetworkTCP, N.NetworkUDP}, nil),
		logger:  logger.NOP(),
		dialer:  staticVLESSDialer{conn: conn},
		client:  client,
		xudp:    true,
	}
}

func TestVLESSDialerClosesXUDPConnectionOnSetupError(t *testing.T) {
	destination := M.Socksaddr{Fqdn: "example.com", Port: 443}
	tests := []struct {
		name string
		run  func(*vlessDialer) error
	}{
		{
			name: "dial context",
			run: func(dialer *vlessDialer) error {
				_, err := dialer.DialContext(context.Background(), N.NetworkUDP, destination)
				return err
			},
		},
		{
			name: "listen packet",
			run: func(dialer *vlessDialer) error {
				_, err := dialer.ListenPacket(context.Background(), destination)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sentinel := errors.New("write failed")
			conn := &failingVLESSConn{writeErr: sentinel}
			err := test.run(newXUDPTestDialer(t, conn))
			if !errors.Is(err, sentinel) {
				t.Fatalf("expected setup error, got %v", err)
			}
			if !conn.closed {
				t.Fatal("setup error left connection open")
			}
		})
	}
}
