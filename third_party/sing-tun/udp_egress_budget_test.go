package tun

import (
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
)

type egressTestFinder struct {
	control.InterfaceFinder
	interfaces []control.Interface
}

func (f *egressTestFinder) Interfaces() []control.Interface { return f.interfaces }

func TestUDPEgressMemberBudgetLifecycle(t *testing.T) {
	var active atomic.Int32
	var deny atomic.Bool
	var failListen atomic.Bool
	finder := &egressTestFinder{}
	pool := NewUDPEgressPool(UDPEgressPoolOptions{
		Logger: logger.NOP(), InterfaceFinder: finder,
		IsExempt: func() bool { return false },
		ReserveSocket: func() (func(), error) {
			if deny.Load() {
				return nil, errors.New("budget denied")
			}
			active.Add(1)
			return func() { active.Add(-1) }, nil
		},
		Control: func(string, string, syscall.RawConn) error {
			if failListen.Load() {
				return errors.New("listen failed")
			}
			return nil
		},
	})
	spec := udpEgressSpec{interfaceName: "loopback", interfaceIndex: 1, prefix: netip.MustParsePrefix("127.0.0.1/8")}
	pool.port = 1
	failListen.Store(true)
	if _, err := pool.listenMember(spec); err == nil || active.Load() != 0 {
		t.Fatal("listen failure leaked reservation")
	}
	failListen.Store(false)
	deny.Store(true)
	if _, err := pool.listenMember(spec); err == nil || active.Load() != 0 {
		t.Fatal("budget rejection did not prevent listen")
	}
	deny.Store(false)
	pool.port = 0
	member, err := pool.listenMember(spec)
	if err != nil {
		t.Fatal(err)
	}
	if active.Load() != 1 {
		t.Fatal("opened member did not retain reservation")
	}
	pool.members[spec] = member
	pool.port = 1
	pool.rebuildLocked()
	if active.Load() != 0 {
		t.Fatal("rebuild removal leaked reservation")
	}
	member, err = pool.listenMember(spec)
	if err != nil {
		t.Fatal(err)
	}
	pool.members[spec] = member
	pool.Close()
	pool.Close()
	if active.Load() != 0 {
		t.Fatal("pool close leaked reservation")
	}
	if _, err = member.conn.WriteToUDP([]byte{1}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}); err == nil {
		t.Fatal("pool close left socket open")
	}
}
