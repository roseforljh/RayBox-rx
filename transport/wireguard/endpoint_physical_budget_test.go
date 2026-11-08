package wireguard

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/roseforljh/RayBox-rx/common/dialer"
	"github.com/sagernet/wireguard-go/conn"
)

func TestReserveEgressSocketLifecycle(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	release, err := dialer.ReservePhysicalSocket()
	if err != nil {
		t.Fatal(err)
	}
	if opened := dialer.CurrentPhysicalBudgetSnapshot(); opened.Active != before.Active+1 {
		t.Fatalf("egress reservation not counted: before=%+v opened=%+v", before, opened)
	}
	release()
	release()
	if after := dialer.CurrentPhysicalBudgetSnapshot(); after.Active != before.Active {
		t.Fatalf("egress reservation leaked: before=%+v after=%+v", before, after)
	}
}

func TestBudgetedWireGuardBindEgressReceiverNotCountedAsSocket(t *testing.T) {
	for _, anchors := range []int{1, 2} {
		before := dialer.CurrentPhysicalBudgetSnapshot()
		bind := newBudgetedWireGuardBind(&testWireGuardBind{receiveCount: anchors + 1}, true)
		functions, _, err := bind.Open(0)
		if err != nil || len(functions) != anchors+1 {
			t.Fatalf("anchors=%d open: functions=%d err=%v", anchors, len(functions), err)
		}
		opened := dialer.CurrentPhysicalBudgetSnapshot()
		if opened.Active != before.Active+anchors {
			t.Fatalf("anchors=%d incorrect reservation: before=%+v opened=%+v", anchors, before, opened)
		}
		_ = bind.Close()
		if after := dialer.CurrentPhysicalBudgetSnapshot(); after.Active != before.Active {
			t.Fatalf("anchors=%d leaked reservation: before=%+v after=%+v", anchors, before, after)
		}
	}
}

func TestBudgetedWireGuardBindOpenCloseLifecycle(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	base := &testWireGuardBind{receiveCount: 2}
	bind := newBudgetedWireGuardBind(base, false)
	receiveFunctions, port, err := bind.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(receiveFunctions) != 2 || port != testWireGuardPort {
		t.Fatalf("unexpected open result: count=%d port=%d", len(receiveFunctions), port)
	}
	opened := dialer.CurrentPhysicalBudgetSnapshot()
	if opened.Active != before.Active+2 {
		t.Fatalf("WireGuard sockets were not budgeted: before=%+v opened=%+v", before, opened)
	}
	if err = bind.Close(); err != nil {
		t.Fatal(err)
	}
	_ = bind.Close()
	after := dialer.CurrentPhysicalBudgetSnapshot()
	if after.Active != before.Active {
		t.Fatalf("WireGuard close leaked active leases: before=%+v after=%+v", before, after)
	}
}

func TestBudgetedWireGuardBindOpenFailureReleasesReservations(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	base := &testWireGuardBind{openErr: errors.New("open failed")}
	bind := newBudgetedWireGuardBind(base, false)
	if _, _, err := bind.Open(0); err == nil {
		t.Fatal("expected open failure")
	}
	after := dialer.CurrentPhysicalBudgetSnapshot()
	if after.Active != before.Active {
		t.Fatalf("failed WireGuard open leaked active leases: before=%+v after=%+v", before, after)
	}
}

func TestBudgetedWireGuardBindOptionalInterfaces(t *testing.T) {
	baseOnly := newBudgetedWireGuardBind(&testWireGuardBind{receiveCount: 1}, false)
	if _, loaded := baseOnly.(conn.PeekLookAtSocketFd); loaded {
		t.Fatal("base wrapper falsely exposes PeekLookAtSocketFd")
	}
	if _, loaded := baseOnly.(conn.BindSocketToInterface); loaded {
		t.Fatal("base wrapper falsely exposes BindSocketToInterface")
	}

	peekSource := &testWireGuardPeekBind{testWireGuardBind: &testWireGuardBind{receiveCount: 1}}
	peekWrapped := newBudgetedWireGuardBind(peekSource, false)
	peek, loaded := peekWrapped.(conn.PeekLookAtSocketFd)
	if !loaded {
		t.Fatal("peek wrapper lost PeekLookAtSocketFd")
	}
	if _, loaded = peekWrapped.(conn.BindSocketToInterface); loaded {
		t.Fatal("peek wrapper falsely exposes BindSocketToInterface")
	}
	if fd, err := peek.PeekLookAtSocketFd4(); err != nil || fd != 41 {
		t.Fatalf("peek4 was not forwarded: fd=%d err=%v", fd, err)
	}

	interfaceSource := &testWireGuardInterfaceBind{testWireGuardBind: &testWireGuardBind{receiveCount: 1}}
	interfaceWrapped := newBudgetedWireGuardBind(interfaceSource, false)
	interfaceBind, loaded := interfaceWrapped.(conn.BindSocketToInterface)
	if !loaded {
		t.Fatal("interface wrapper lost BindSocketToInterface")
	}
	if _, loaded = interfaceWrapped.(conn.PeekLookAtSocketFd); loaded {
		t.Fatal("interface wrapper falsely exposes PeekLookAtSocketFd")
	}
	if err := interfaceBind.BindSocketToInterface4(7, true); err != nil {
		t.Fatal(err)
	}
	if interfaceSource.lastInterface != 7 || !interfaceSource.lastBlackhole {
		t.Fatal("BindSocketToInterface4 parameters were not forwarded")
	}

	combinedSource := &testWireGuardCombinedBind{
		testWireGuardBind: &testWireGuardBind{receiveCount: 1},
	}
	combinedWrapped := newBudgetedWireGuardBind(combinedSource, false)
	if _, loaded = combinedWrapped.(conn.PeekLookAtSocketFd); !loaded {
		t.Fatal("combined wrapper lost PeekLookAtSocketFd")
	}
	if _, loaded = combinedWrapped.(conn.BindSocketToInterface); !loaded {
		t.Fatal("combined wrapper lost BindSocketToInterface")
	}
}

const testWireGuardPort = 51820

type testWireGuardBind struct {
	receiveCount int
	openErr      error
	closed       bool
}

func (b *testWireGuardBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	if b.openErr != nil {
		return nil, 0, b.openErr
	}
	receiveFunctions := make([]conn.ReceiveFunc, b.receiveCount)
	for index := range receiveFunctions {
		receiveFunctions[index] = func([][]byte, []int, []conn.Endpoint) (int, error) { return 0, nil }
	}
	return receiveFunctions, testWireGuardPort, nil
}

func (b *testWireGuardBind) Close() error {
	b.closed = true
	return nil
}

func (*testWireGuardBind) SetMark(uint32) error                           { return nil }
func (*testWireGuardBind) Send([][]byte, conn.Endpoint, int) error        { return nil }
func (*testWireGuardBind) ParseEndpoint(string) (conn.Endpoint, error)    { return nil, nil }
func (*testWireGuardBind) BatchSize() int                                 { return 1 }
func (*testWireGuardBind) SetReservedForEndpoint(netip.AddrPort, [3]byte) {}

type testWireGuardPeekBind struct {
	*testWireGuardBind
}

func (*testWireGuardPeekBind) PeekLookAtSocketFd4() (int, error) { return 41, nil }
func (*testWireGuardPeekBind) PeekLookAtSocketFd6() (int, error) { return 61, nil }

type testWireGuardInterfaceBind struct {
	*testWireGuardBind
	lastInterface uint32
	lastBlackhole bool
}

func (b *testWireGuardInterfaceBind) BindSocketToInterface4(index uint32, blackhole bool) error {
	b.lastInterface = index
	b.lastBlackhole = blackhole
	return nil
}

func (b *testWireGuardInterfaceBind) BindSocketToInterface6(index uint32, blackhole bool) error {
	b.lastInterface = index
	b.lastBlackhole = blackhole
	return nil
}

type testWireGuardCombinedBind struct {
	*testWireGuardBind
}

func (*testWireGuardCombinedBind) PeekLookAtSocketFd4() (int, error)         { return 42, nil }
func (*testWireGuardCombinedBind) PeekLookAtSocketFd6() (int, error)         { return 62, nil }
func (*testWireGuardCombinedBind) BindSocketToInterface4(uint32, bool) error { return nil }
func (*testWireGuardCombinedBind) BindSocketToInterface6(uint32, bool) error { return nil }
