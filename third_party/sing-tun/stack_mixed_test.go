//go:build with_gvisor

package tun

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
)

func TestMixedCloseWithoutGVisorStackClosesSystem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := newSystemAcceptTestListener(net.IPv4(127, 0, 0, 1))
	system := &System{
		ctx:                ctx,
		logger:             logger.NOP(),
		tcpListener:        listener,
		tcpNat4:            NewNat(ctx, time.Minute),
		tcpConnections:     make(map[*systemTCPConnection]struct{}),
		tcpConnectionLimit: 1,
		closeSignal:        make(chan struct{}),
	}
	system.startAcceptLoop(listener, system.tcpNat4)
	mixed := &Mixed{System: system}

	if err := mixed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mixed.Close(); err != nil {
		t.Fatal("second close returned a different result: ", err)
	}
	system.tcpConnectionAccess.Lock()
	closed := system.closed
	system.tcpConnectionAccess.Unlock()
	if !closed {
		t.Fatal("mixed close did not close the system stack")
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("mixed close left the system listener open")
	}
}
