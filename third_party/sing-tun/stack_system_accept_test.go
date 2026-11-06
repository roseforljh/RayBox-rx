package tun

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type systemAcceptTestHandler struct {
	Handler
	connections chan systemAcceptTestConnection
}

type systemAcceptTestConnection struct {
	conn    net.Conn
	onClose N.CloseHandlerFunc
}

func (h *systemAcceptTestHandler) NewConnectionEx(
	_ context.Context,
	conn net.Conn,
	_ M.Socksaddr,
	_ M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
	h.connections <- systemAcceptTestConnection{conn: conn, onClose: onClose}
}

func addSystemNATSession(nat *TCPNat, port uint16, source, destination netip.AddrPort) {
	nat.portAccess.Lock()
	nat.portMap[port] = &TCPSession{
		Source:      source,
		Destination: destination,
		LastActive:  time.Now(),
	}
	nat.portAccess.Unlock()
}

func removeSystemNATSession(nat *TCPNat, port uint16) {
	nat.addrAccess.Lock()
	nat.portAccess.Lock()
	session := nat.portMap[port]
	if session != nil {
		delete(nat.portMap, port)
		delete(nat.addrMap, tcpNatKey{Source: session.Source, Destination: session.Destination})
	}
	nat.portAccess.Unlock()
	nat.addrAccess.Unlock()
}

type systemAcceptTestListener struct {
	connections  chan net.Conn
	acceptErrors chan error
	closed       chan struct{}
	closeOnce    sync.Once
	address      *net.TCPAddr
}

func newSystemAcceptTestListener(ip net.IP) *systemAcceptTestListener {
	return &systemAcceptTestListener{
		connections:  make(chan net.Conn, 4),
		acceptErrors: make(chan error),
		closed:       make(chan struct{}),
		address:      &net.TCPAddr{IP: ip},
	}
}

func (l *systemAcceptTestListener) Accept() (net.Conn, error) {
	select {
	case err := <-l.acceptErrors:
		return nil, err
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *systemAcceptTestListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *systemAcceptTestListener) Addr() net.Addr {
	return l.address
}

type systemAcceptTestConn struct {
	net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (c *systemAcceptTestConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *systemAcceptTestConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

type systemCapabilityTestConn struct {
	net.Conn
	closeCount      atomic.Int32
	closeReadCount  atomic.Int32
	closeWriteCount atomic.Int32
}

func (c *systemCapabilityTestConn) Close() error {
	c.closeCount.Add(1)
	return c.Conn.Close()
}

func (c *systemCapabilityTestConn) CloseRead() error {
	c.closeReadCount.Add(1)
	return nil
}

func (c *systemCapabilityTestConn) CloseWrite() error {
	c.closeWriteCount.Add(1)
	return nil
}

type systemLateAcceptListener struct {
	conn      net.Conn
	started   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	accepts   atomic.Int32
	closeOnce sync.Once
	localAddr net.Addr
}

func (l *systemLateAcceptListener) Accept() (net.Conn, error) {
	if l.accepts.Add(1) == 1 {
		close(l.started)
		<-l.release
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *systemLateAcceptListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *systemLateAcceptListener) Addr() net.Addr {
	return l.localAddr
}

func TestSystemTCPConnectionCloseOnceAndPreservesCapabilities(t *testing.T) {
	accepted, peer := net.Pipe()
	defer peer.Close()
	upstream := &systemCapabilityTestConn{Conn: accepted}
	system := &System{
		tcpConnections:     make(map[*systemTCPConnection]struct{}),
		tcpConnectionLimit: 1,
		closeSignal:        make(chan struct{}),
	}
	tracked := system.trackTCPConnection(upstream)
	if tracked == nil {
		t.Fatal("connection was not admitted")
	}
	if tracked.Upstream() != upstream || !tracked.ReaderReplaceable() || !tracked.WriterReplaceable() {
		t.Fatal("tracked connection did not preserve its upstream fast path")
	}
	if err := tracked.CloseRead(); err != nil {
		t.Fatal(err)
	}
	if err := tracked.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if upstream.closeReadCount.Load() != 1 || upstream.closeWriteCount.Load() != 1 {
		t.Fatal("tracked connection did not forward half-close capabilities")
	}
	system.tcpConnectionAccess.Lock()
	activeBeforeClose := len(system.tcpConnections)
	system.tcpConnectionAccess.Unlock()
	if activeBeforeClose != 1 {
		t.Fatal("half-close released the active connection slot")
	}

	var closeWait sync.WaitGroup
	for range 100 {
		closeWait.Add(1)
		go func() {
			defer closeWait.Done()
			_ = tracked.Close()
		}()
	}
	closeWait.Wait()
	if upstream.closeCount.Load() != 1 {
		t.Fatalf("underlying connection closed %d times", upstream.closeCount.Load())
	}
	system.tcpConnectionAccess.Lock()
	activeAfterClose := len(system.tcpConnections)
	system.tcpConnectionAccess.Unlock()
	if activeAfterClose != 0 {
		t.Fatalf("active connection slot was not released: %d", activeAfterClose)
	}
}

func TestSystemCloseRejectsLateAcceptBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted, peer := net.Pipe()
	defer peer.Close()
	localAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	listener := &systemLateAcceptListener{
		conn: &systemAcceptTestConn{
			Conn:       accepted,
			localAddr:  localAddr,
			remoteAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10001},
		},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		closed:    make(chan struct{}),
		localAddr: localAddr,
	}
	handler := &systemAcceptTestHandler{connections: make(chan systemAcceptTestConnection, 1)}
	system := &System{
		ctx:                ctx,
		handler:            handler,
		logger:             logger.NOP(),
		tcpListener:        listener,
		tcpNat4:            NewNat(ctx, time.Minute),
		tcpConnections:     make(map[*systemTCPConnection]struct{}),
		tcpConnectionLimit: 1,
		closeSignal:        make(chan struct{}),
	}
	addSystemNATSession(
		system.tcpNat4,
		10001,
		netip.MustParseAddrPort("10.0.0.1:1234"),
		netip.MustParseAddrPort("1.1.1.1:443"),
	)
	system.startAcceptLoop(listener, system.tcpNat4)
	<-listener.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- system.Close() }()
	<-system.closeSignal
	close(listener.release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("system close did not wait for the accept loop")
	}
	select {
	case <-handler.connections:
		t.Fatal("handler received a connection after system close started")
	default:
	}
	deadlineErr := peer.SetReadDeadline(time.Now().Add(time.Second))
	if deadlineErr != nil && !errors.Is(deadlineErr, io.ErrClosedPipe) {
		t.Fatal(deadlineErr)
	}
	if deadlineErr == nil {
		if _, err := peer.Read(make([]byte, 1)); err == nil {
			t.Fatal("late accepted connection remained open")
		}
	}
	if system.tcpNat4.LookupBack(10001) == nil {
		t.Fatal("late rejected connection lost its NAT teardown mapping")
	}
}

func TestSystemAcceptLoopClosesUnknownSession(t *testing.T) {
	previousGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(previousGCPercent)

	ctx, cancel := context.WithCancel(context.Background())
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	system := &System{
		ctx:            ctx,
		logger:         logger.NOP(),
		tcpListener:    listener,
		tcpNat4:        NewNat(ctx, time.Minute),
		tcpConnections: make(map[*systemTCPConnection]struct{}),
		closeSignal:    make(chan struct{}),
	}
	system.startAcceptLoop(listener, system.tcpNat4)
	t.Cleanup(func() {
		_ = system.Close()
		cancel()
	})

	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("unknown NAT session remained connected")
	}
	if netErr, isNetworkError := err.(net.Error); isNetworkError && netErr.Timeout() {
		t.Fatal("accepted TCP socket was not closed for an unknown NAT session")
	}
}

func TestSystemAcceptLoopLimitsActiveConnections(t *testing.T) {
	if systemTCPConnectionLimit != 4096 {
		t.Fatalf("unexpected production connection limit: %d", systemTCPConnectionLimit)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener4 := newSystemAcceptTestListener(net.IPv4(127, 0, 0, 1))
	listener6 := newSystemAcceptTestListener(net.IPv6loopback)
	handler := &systemAcceptTestHandler{connections: make(chan systemAcceptTestConnection, 3)}
	system := &System{
		ctx:                ctx,
		handler:            handler,
		logger:             logger.NOP(),
		tcpListener:        listener4,
		tcpListener6:       listener6,
		tcpNat4:            NewNat(ctx, time.Minute),
		tcpNat6:            NewNat(ctx, time.Minute),
		tcpConnections:     make(map[*systemTCPConnection]struct{}),
		tcpConnectionLimit: 2,
		closeSignal:        make(chan struct{}),
	}
	system.startAcceptLoop(listener4, system.tcpNat4)
	system.startAcceptLoop(listener6, system.tcpNat6)

	peers := make([]net.Conn, 0, 4)
	queueConnection := func(listener *systemAcceptTestListener, port uint16) net.Conn {
		nat := system.tcpNat4
		if listener == listener6 {
			nat = system.tcpNat6
		}
		addSystemNATSession(
			nat,
			port,
			netip.MustParseAddrPort("10.0.0.1:1234"),
			netip.MustParseAddrPort("1.1.1.1:443"),
		)
		accepted, peer := net.Pipe()
		if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		peers = append(peers, peer)
		listener.connections <- &systemAcceptTestConn{
			Conn:       accepted,
			localAddr:  listener.address,
			remoteAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)},
		}
		return peer
	}
	nextAccepted := func() systemAcceptTestConnection {
		select {
		case connection := <-handler.connections:
			return connection
		case <-time.After(time.Second):
			t.Fatal("accepted connection was not forwarded")
			return systemAcceptTestConnection{}
		}
	}
	assertPeerClosed := func(peer net.Conn) {
		t.Helper()
		_, err := peer.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("connection remained open")
		}
		if netErr, isNetworkError := err.(net.Error); isNetworkError && netErr.Timeout() {
			t.Fatal("connection was not closed before the deadline")
		}
	}

	queueConnection(listener4, 10001)
	first := nextAccepted()
	queueConnection(listener6, 10002)
	second := nextAccepted()
	rejectedPeer := queueConnection(listener6, 10003)
	assertPeerClosed(rejectedPeer)
	if session := system.tcpNat6.LookupBack(10003); session == nil {
		t.Fatal("rejected connection lost its NAT session before RST translation")
	}
	system.tcpConnectionAccess.Lock()
	rejected := system.tcpRejected
	system.tcpConnectionAccess.Unlock()
	if rejected != 1 {
		t.Fatalf("unexpected rejection count: %d", rejected)
	}

	trackedFirst, isTrackedConnection := first.conn.(*systemTCPConnection)
	if !isTrackedConnection {
		t.Fatal("accepted connection was not tracked before reaching the handler")
	}
	if _, isOriginalConn := trackedFirst.Upstream().(*systemAcceptTestConn); !isOriginalConn {
		t.Fatal("tracked connection did not preserve the original upstream")
	}
	if err := first.conn.Close(); err != nil {
		t.Fatal(err)
	}
	first.onClose(nil)
	first.onClose(nil)
	if session := system.tcpNat4.LookupBack(10001); session == nil {
		t.Fatal("closed connection lost its NAT session before FIN translation")
	}
	queueConnection(listener4, 10004)
	fourth := nextAccepted()

	concurrentCloseDone := make(chan struct{})
	go func() {
		_ = fourth.conn.Close()
		fourth.onClose(nil)
		close(concurrentCloseDone)
	}()
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}
	if err := system.Close(); err != nil {
		t.Fatal("second close returned a different result: ", err)
	}
	select {
	case <-concurrentCloseDone:
	case <-time.After(time.Second):
		t.Fatal("connection shutdown did not finish")
	}
	second.onClose(nil)
	second.onClose(nil)
	system.tcpConnectionAccess.Lock()
	active := len(system.tcpConnections)
	system.tcpConnectionAccess.Unlock()
	if active != 0 {
		t.Fatalf("active connection slots were not released: %d", active)
	}
	assertPeerClosed(peers[1])
	assertPeerClosed(peers[3])
	for port, nat := range map[uint16]*TCPNat{
		10001: system.tcpNat4,
		10002: system.tcpNat6,
		10003: system.tcpNat6,
		10004: system.tcpNat4,
	} {
		if nat.LookupBack(port) == nil {
			t.Fatalf("system close removed NAT session %d before teardown translation", port)
		}
	}
}

func TestSystemAcceptLoopRetriesFileDescriptorExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := newSystemAcceptTestListener(net.IPv4(127, 0, 0, 1))
	handler := &systemAcceptTestHandler{connections: make(chan systemAcceptTestConnection, 1)}
	system := &System{
		ctx:                ctx,
		handler:            handler,
		logger:             logger.NOP(),
		tcpListener:        listener,
		tcpNat4:            NewNat(ctx, time.Minute),
		tcpConnections:     make(map[*systemTCPConnection]struct{}),
		tcpConnectionLimit: 1,
		closeSignal:        make(chan struct{}),
	}
	system.startAcceptLoop(listener, system.tcpNat4)

	listener.acceptErrors <- syscall.EMFILE
	addSystemNATSession(
		system.tcpNat4,
		10001,
		netip.MustParseAddrPort("10.0.0.1:1234"),
		netip.MustParseAddrPort("1.1.1.1:443"),
	)
	accepted, peer := net.Pipe()
	defer peer.Close()
	listener.connections <- &systemAcceptTestConn{
		Conn:       accepted,
		localAddr:  listener.address,
		remoteAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10001},
	}
	var connection systemAcceptTestConnection
	select {
	case connection = <-handler.connections:
	case <-time.After(time.Second):
		t.Fatal("accept loop did not recover after EMFILE")
	}
	if err := connection.conn.Close(); err != nil {
		t.Fatal(err)
	}
	connection.onClose(nil)
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTCPNatDoesNotOverwriteActivePortsAfterWrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nat := NewNat(ctx, time.Minute)
	source := netip.MustParseAddrPort("192.0.2.1:1234")
	activePorts := make(map[uint16]struct{}, systemTCPConnectionLimit)
	for index := range systemTCPConnectionLimit {
		destination := netip.AddrPortFrom(
			netip.AddrFrom4([4]byte{10, byte(index >> 16), byte(index >> 8), byte(index)}),
			443,
		)
		port := nat.Lookup(source, destination)
		if port == 0 {
			t.Fatal("failed to allocate active NAT port")
		}
		activePorts[port] = struct{}{}
	}
	for index := range 70_000 {
		destination := netip.AddrPortFrom(
			netip.AddrFrom4([4]byte{11, byte(index >> 16), byte(index >> 8), byte(index)}),
			443,
		)
		port := nat.Lookup(source, destination)
		if port == 0 {
			t.Fatal("failed to allocate churn NAT port")
		}
		if _, overwroteActivePort := activePorts[port]; overwroteActivePort {
			t.Fatalf("active NAT port was overwritten after wrap: %d", port)
		}
		removeSystemNATSession(nat, port)
	}
	if len(nat.portMap) != systemTCPConnectionLimit || len(nat.addrMap) != systemTCPConnectionLimit {
		t.Fatalf("unexpected NAT size after churn: ports=%d addresses=%d", len(nat.portMap), len(nat.addrMap))
	}
	for port := range activePorts {
		removeSystemNATSession(nat, port)
	}
	if len(nat.portMap) != 0 || len(nat.addrMap) != 0 {
		t.Fatal("released NAT sessions were retained")
	}
}
