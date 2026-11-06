package route

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	udpnat "github.com/sagernet/sing/common/udpnat2"
)

func TestStreamDialFloodStaysBoundedBeforeHandshake(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().Logger())
	dialer := &countingBlockingPacketDialer{}
	ctx, cancel := context.WithCancel(context.Background())
	const attempts = 4096
	var waitGroup sync.WaitGroup
	for range attempts {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			manager.NewConnection(
				ctx,
				dialer,
				&discardStreamConn{},
				adapter.InboundContext{Destination: M.ParseSocksaddr("1.1.1.1:443")},
				nil,
			)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.connectionRejected.Load() < attempts-maxActivePreHandshakeConnections &&
		time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	peak := dialer.peak.Load()
	cancel()
	waitGroup.Wait()
	if peak > maxActivePreHandshakeConnections {
		t.Fatalf("stream dial flood exceeded pre-handshake capacity: peak=%d", peak)
	}
}

func TestPacketDialCanceledWhenInboundNATExpires(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().Logger())
	source := &expiringPacketConn{}
	dialer := &blockingPacketDialer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		manager.NewPacketConnection(
			context.Background(),
			dialer,
			source,
			adapter.InboundContext{Destination: M.ParseSocksaddr("1.1.1.1:443")},
			nil,
		)
		close(done)
	}()

	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("packet dial did not start")
	}
	source.expired.Store(true)
	select {
	case <-dialer.canceled:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expired inbound NAT did not cancel the pending packet dial")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("packet connection did not return after dial cancellation")
	}
}

func TestPacketDialFloodStaysBoundedByInboundNATCapacity(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().Logger())
	dialer := &countingBlockingPacketDialer{}
	handler := &natPacketRouteHandler{manager: manager, dialer: dialer}
	service := udpnat.New(
		handler,
		func(_, _ M.Socksaddr, _ any) (bool, context.Context, N.PacketWriter, N.CloseHandlerFunc) {
			return true, context.Background(), discardPacketWriter{}, nil
		},
		time.Minute,
		true,
	)
	destination := M.Socksaddr{Addr: netip.MustParseAddr("1.1.1.1"), Port: 443}
	for index := 0; index < 32_768; index++ {
		source := M.Socksaddr{
			Addr: netip.AddrFrom4([4]byte{10, byte(index >> 8), byte(index), 1}),
			Port: uint16(index + 1),
		}
		service.NewPacket([][]byte{{1}}, source, destination, nil)
	}
	deadline := time.Now().Add(5 * time.Second)
	for dialer.started.Load() < 1024 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	service.Purge()
	for dialer.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active := dialer.active.Load(); active != 0 {
		t.Fatalf("packet dials still active after NAT purge: %d", active)
	}
	if peak := dialer.peak.Load(); peak > 1024 {
		t.Fatalf("packet dial flood exceeded NAT capacity: peak=%d", peak)
	}
}

type natPacketRouteHandler struct {
	manager *ConnectionManager
	dialer  *countingBlockingPacketDialer
}

func (h *natPacketRouteHandler) NewPacketConnectionEx(
	ctx context.Context,
	conn N.PacketConn,
	_ M.Socksaddr,
	destination M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
	h.manager.NewPacketConnection(
		ctx,
		h.dialer,
		conn,
		adapter.InboundContext{Destination: destination},
		onClose,
	)
}

type discardPacketWriter struct{}

func (discardPacketWriter) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}

type discardStreamConn struct{}

func (*discardStreamConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (*discardStreamConn) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (*discardStreamConn) Close() error                     { return nil }
func (*discardStreamConn) LocalAddr() net.Addr              { return M.Socksaddr{} }
func (*discardStreamConn) RemoteAddr() net.Addr             { return M.Socksaddr{} }
func (*discardStreamConn) SetDeadline(time.Time) error      { return nil }
func (*discardStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (*discardStreamConn) SetWriteDeadline(time.Time) error { return nil }

type countingBlockingPacketDialer struct {
	started atomic.Int64
	active  atomic.Int64
	peak    atomic.Int64
}

func (d *countingBlockingPacketDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	return nil, d.wait(ctx)
}

func (d *countingBlockingPacketDialer) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	return nil, d.wait(ctx)
}

func (d *countingBlockingPacketDialer) wait(ctx context.Context) error {
	active := d.active.Add(1)
	d.started.Add(1)
	for peak := d.peak.Load(); active > peak && !d.peak.CompareAndSwap(peak, active); peak = d.peak.Load() {
	}
	<-ctx.Done()
	d.active.Add(-1)
	return ctx.Err()
}

type expiringPacketConn struct {
	expired atomic.Bool
}

func (c *expiringPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, net.ErrClosed
}

func (c *expiringPacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}

func (c *expiringPacketConn) Close() error                     { return nil }
func (c *expiringPacketConn) LocalAddr() net.Addr              { return M.Socksaddr{} }
func (c *expiringPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *expiringPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *expiringPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *expiringPacketConn) Timeout() time.Duration {
	if c.expired.Load() {
		return 0
	}
	return time.Minute
}

func (c *expiringPacketConn) SetTimeout(time.Duration) bool { return true }

type blockingPacketDialer struct {
	started  chan struct{}
	canceled chan struct{}
}

func (d *blockingPacketDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	return nil, d.wait(ctx)
}

func (d *blockingPacketDialer) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	return nil, d.wait(ctx)
}

func (d *blockingPacketDialer) wait(ctx context.Context) error {
	close(d.started)
	<-ctx.Done()
	close(d.canceled)
	return ctx.Err()
}
