package route

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/sniff"
	"github.com/sagernet/sing-box/common/tlsfragment"
	"github.com/sagernet/sing-box/common/tlsspoof"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/canceler"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
)

var _ adapter.ConnectionManager = (*ConnectionManager)(nil)

const (
	maxActivePreHandshakeConnections = 1024
	processConnectionFDStopCount     = 8192
	processConnectionFDResumeCount   = 4096
	processFDCheckEveryAttempts      = 32
	processFDCheckMinInterval        = 250 * time.Millisecond
)

var (
	errConnectionCapacity = E.New("pre-handshake connection capacity reached")
	errProcessFDBudget    = E.New("process file descriptor safety budget reached")
)

type ConnectionManager struct {
	connectionSlots            chan struct{}
	connectionAttempts         atomic.Uint64
	connectionRejected         atomic.Uint64
	lastProcessFDCount         atomic.Int64
	lastProcessFDCheckAtNanos  atomic.Int64
	processFDBlocked           atomic.Bool
	lastPhysicalBudgetLogNanos atomic.Int64
	logger                     logger.ContextLogger
	access                     sync.Mutex
	connections                list.List[io.Closer]
}

func NewConnectionManager(logger logger.ContextLogger) *ConnectionManager {
	return &ConnectionManager{
		logger:          logger,
		connectionSlots: make(chan struct{}, maxActivePreHandshakeConnections),
	}
}

func (m *ConnectionManager) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateInitialize {
		return nil
	}
	scope.Add(func() error {
		m.CloseAll()
		return nil
	})
	return nil
}

func (m *ConnectionManager) Count() int {
	return m.connections.Len()
}

func (m *ConnectionManager) CloseAll() {
	m.access.Lock()
	var closers []io.Closer
	for element := m.connections.Front(); element != nil; {
		nextElement := element.Next()
		closers = append(closers, element.Value)
		m.connections.Remove(element)
		element = nextElement
	}
	m.access.Unlock()
	for _, closer := range closers {
		common.Close(closer)
	}
}

func (m *ConnectionManager) TrackConn(conn net.Conn) net.Conn {
	m.access.Lock()
	element := m.connections.PushBack(conn)
	m.access.Unlock()
	return &trackedConn{
		Conn:    conn,
		manager: m,
		element: element,
	}
}

func (m *ConnectionManager) TrackPacketConn(conn net.PacketConn) net.PacketConn {
	m.access.Lock()
	element := m.connections.PushBack(conn)
	m.access.Unlock()
	return &trackedPacketConn{
		PacketConn: conn,
		manager:    m,
		element:    element,
	}
}

func (m *ConnectionManager) NewConnection(ctx context.Context, this N.Dialer, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if applyKunBoxProxyDestinationOverride(&metadata, this) {
		m.logger.DebugContext(ctx, "kunbox override destination: ", metadata.RouteOriginalDestination, " => ", metadata.Destination)
	}
	ctx = adapter.WithContext(ctx, &metadata)
	defer m.logPhysicalBudgetSnapshot(ctx)
	if err := m.tryAcquireConnection(); err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		m.logConnectionRejection(ctx, this, N.NetworkTCP, metadata, err)
		return
	}
	onClose = m.manageConnectionClose(onClose)
	var (
		remoteConn net.Conn
		err        error
	)
	if len(metadata.DestinationAddresses) > 0 || metadata.Destination.IsIP() {
		remoteConn, err = dialer.DialSerialNetwork(ctx, this, N.NetworkTCP, metadata.Destination, metadata.DestinationAddresses, metadata.NetworkStrategy, metadata.NetworkType, metadata.FallbackNetworkType, metadata.FallbackDelay)
	} else {
		remoteConn, err = this.DialContext(ctx, N.NetworkTCP, metadata.Destination)
	}
	if err != nil {
		var remoteString string
		if len(metadata.DestinationAddresses) > 0 {
			remoteString = "[" + strings.Join(common.Map(metadata.DestinationAddresses, netip.Addr.String), ",") + "]"
		} else {
			remoteString = metadata.Destination.String()
		}
		var dialerString string
		if outbound, isOutbound := this.(adapter.Outbound); isOutbound {
			dialerString = " using outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
		}
		err = E.Cause(err, "open connection to ", remoteString, dialerString)
		N.CloseOnHandshakeFailure(conn, onClose, err)
		m.logger.ErrorContext(ctx, err)
		return
	}
	err = N.ReportConnHandshakeSuccess(conn, remoteConn)
	if err != nil {
		err = E.Cause(err, "report handshake success")
		remoteConn.Close()
		N.CloseOnHandshakeFailure(conn, onClose, err)
		m.logger.ErrorContext(ctx, err)
		return
	}
	if metadata.TLSFragment || metadata.TLSRecordFragment {
		remoteConn = tf.NewConn(remoteConn, ctx, metadata.TLSFragment, metadata.TLSRecordFragment, metadata.TLSFragmentFallbackDelay)
	}
	if metadata.TLSSpoof != "" {
		spoofConn, spoofErr := tlsspoof.NewConn(remoteConn, metadata.TLSSpoofMethod, metadata.TLSSpoof)
		if spoofErr != nil {
			spoofErr = E.Cause(spoofErr, "tls_spoof setup")
			remoteConn.Close()
			N.CloseOnHandshakeFailure(conn, onClose, spoofErr)
			m.logger.ErrorContext(ctx, spoofErr)
			return
		}
		remoteConn = spoofConn
	}
	serverFirst := sniff.Skip(&metadata)
	var done atomic.Bool
	if m.kickWriteHandshake(ctx, conn, remoteConn, serverFirst, false, &done, onClose) {
		return
	}
	if m.kickWriteHandshake(ctx, remoteConn, conn, serverFirst, true, &done, onClose) {
		return
	}
	go m.connectionCopy(ctx, conn, remoteConn, false, &done, onClose)
	go m.connectionCopy(ctx, remoteConn, conn, true, &done, onClose)
}

func applyKunBoxProxyDestinationOverride(metadata *adapter.InboundContext, dialer N.Dialer) bool {
	outbound, isOutbound := dialer.(adapter.Outbound)
	if !isOutbound || !isKunBoxRemoteProxyOutbound(outbound) || metadata.RouteOriginalDestination.IsValid() {
		return false
	}
	if metadata.Network != N.NetworkTCP || !metadata.Destination.IsIP() || !M.IsDomainName(metadata.Domain) {
		return false
	}
	if metadata.Inbound != "tun-in" && metadata.Inbound != "redirect-in-v4" && metadata.Inbound != "redirect-in-v6" &&
		!(strings.HasPrefix(metadata.Inbound, "root-lane-") &&
			(strings.HasSuffix(metadata.Inbound, "-tcp-v4") || strings.HasSuffix(metadata.Inbound, "-tcp-v6"))) {
		return false
	}
	if metadata.Destination.Port != 80 && metadata.Destination.Port != 443 {
		return false
	}
	metadata.RouteOriginalDestination = metadata.Destination
	metadata.Destination = M.Socksaddr{
		Fqdn: metadata.Domain,
		Port: metadata.Destination.Port,
	}
	metadata.DestinationAddresses = nil
	return true
}

func isKunBoxRemoteProxyOutbound(outbound adapter.Outbound) bool {
	if C.IsRemoteProxyType(outbound.Type()) {
		return true
	}
	group, isRemoteOnlyGroup := outbound.(interface{ KunBoxRemoteOnly() bool })
	return isRemoteOnlyGroup && group.KunBoxRemoteOnly()
}

func (m *ConnectionManager) NewPacketConnection(ctx context.Context, this N.Dialer, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = adapter.WithContext(ctx, &metadata)
	defer m.logPhysicalBudgetSnapshot(ctx)
	dialContext, cancelDial := newPacketDialContext(ctx, conn)
	defer cancelDial()
	if err := m.tryAcquireConnection(); err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		m.logConnectionRejection(ctx, this, N.NetworkUDP, metadata, err)
		return
	}
	onClose = m.manageConnectionClose(onClose)
	var (
		remotePacketConn   net.PacketConn
		remoteConn         net.Conn
		destinationAddress netip.Addr
		err                error
	)
	if metadata.UDPConnect {
		parallelDialer, isParallelDialer := this.(dialer.ParallelInterfaceDialer)
		if len(metadata.DestinationAddresses) > 0 {
			if isParallelDialer {
				remoteConn, err = dialer.DialSerialNetwork(dialContext, parallelDialer, N.NetworkUDP, metadata.Destination, metadata.DestinationAddresses, metadata.NetworkStrategy, metadata.NetworkType, metadata.FallbackNetworkType, metadata.FallbackDelay)
			} else {
				remoteConn, err = N.DialSerial(dialContext, this, N.NetworkUDP, metadata.Destination, metadata.DestinationAddresses)
			}
		} else if metadata.Destination.IsIP() {
			if isParallelDialer {
				remoteConn, err = dialer.DialSerialNetwork(dialContext, parallelDialer, N.NetworkUDP, metadata.Destination, metadata.DestinationAddresses, metadata.NetworkStrategy, metadata.NetworkType, metadata.FallbackNetworkType, metadata.FallbackDelay)
			} else {
				remoteConn, err = this.DialContext(dialContext, N.NetworkUDP, metadata.Destination)
			}
		} else {
			remoteConn, err = this.DialContext(dialContext, N.NetworkUDP, metadata.Destination)
		}
		if err != nil {
			var remoteString string
			if len(metadata.DestinationAddresses) > 0 {
				remoteString = "[" + strings.Join(common.Map(metadata.DestinationAddresses, netip.Addr.String), ",") + "]"
			} else {
				remoteString = metadata.Destination.String()
			}
			var dialerString string
			if outbound, isOutbound := this.(adapter.Outbound); isOutbound {
				dialerString = " using outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
			}
			err = E.Cause(err, "open packet connection to ", remoteString, dialerString)
			N.CloseOnHandshakeFailure(conn, onClose, err)
			m.logger.ErrorContext(ctx, err)
			return
		}
		remotePacketConn = bufio.NewUnbindPacketConn(remoteConn)
		connRemoteAddr := M.AddrFromNet(remoteConn.RemoteAddr())
		if connRemoteAddr != metadata.Destination.Addr {
			destinationAddress = connRemoteAddr
		}
	} else {
		if len(metadata.DestinationAddresses) > 0 {
			remotePacketConn, destinationAddress, err = dialer.ListenSerialNetworkPacket(dialContext, this, metadata.Destination, metadata.DestinationAddresses, metadata.NetworkStrategy, metadata.NetworkType, metadata.FallbackNetworkType, metadata.FallbackDelay)
		} else if packetDialer, withDestination := this.(dialer.PacketDialerWithDestination); withDestination {
			remotePacketConn, destinationAddress, err = packetDialer.ListenPacketWithDestination(dialContext, metadata.Destination)
		} else {
			remotePacketConn, err = this.ListenPacket(dialContext, metadata.Destination)
		}
		if err != nil {
			var dialerString string
			if outbound, isOutbound := this.(adapter.Outbound); isOutbound {
				dialerString = " using outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
			}
			err = E.Cause(err, "listen packet connection using ", dialerString)
			N.CloseOnHandshakeFailure(conn, onClose, err)
			m.logger.ErrorContext(ctx, err)
			return
		}
	}
	err = N.ReportPacketConnHandshakeSuccess(conn, remotePacketConn)
	if err != nil {
		conn.Close()
		remotePacketConn.Close()
		onClose(err)
		m.logger.ErrorContext(ctx, "report handshake success: ", err)
		return
	}
	if destinationAddress.IsValid() {
		var originDestination M.Socksaddr
		if metadata.RouteOriginalDestination.IsValid() {
			originDestination = metadata.RouteOriginalDestination
		} else {
			originDestination = metadata.Destination
		}
		if natConn, loaded := common.Cast[bufio.NATPacketConn](conn); loaded {
			natConn.UpdateDestination(destinationAddress)
		} else {
			destination := M.SocksaddrFrom(destinationAddress, metadata.Destination.Port)
			if metadata.Destination != destination {
				if metadata.UDPDisableDomainUnmapping {
					remotePacketConn = bufio.NewUnidirectionalNATPacketConn(bufio.NewPacketConn(remotePacketConn), destination, originDestination)
				} else {
					remotePacketConn = bufio.NewNATPacketConn(bufio.NewPacketConn(remotePacketConn), destination, originDestination)
				}
			} else if metadata.RouteOriginalDestination.IsValid() && metadata.RouteOriginalDestination != metadata.Destination {
				remotePacketConn = bufio.NewDestinationNATPacketConn(bufio.NewPacketConn(remotePacketConn), metadata.Destination, metadata.RouteOriginalDestination)
			}
		}
	} else if metadata.RouteOriginalDestination.IsValid() && metadata.RouteOriginalDestination != metadata.Destination {
		remotePacketConn = bufio.NewDestinationNATPacketConn(bufio.NewPacketConn(remotePacketConn), metadata.Destination, metadata.RouteOriginalDestination)
	}
	var udpTimeout time.Duration
	if metadata.UDPTimeout > 0 {
		udpTimeout = metadata.UDPTimeout
	} else {
		protocol := metadata.Protocol
		if protocol == "" {
			protocol = C.PortProtocols[metadata.Destination.Port]
		}
		if protocol != "" {
			udpTimeout = C.ProtocolTimeouts[protocol]
		}
	}
	if udpTimeout > 0 {
		ctx, conn = canceler.NewPacketConn(ctx, conn, udpTimeout)
	}
	destination := bufio.NewPacketConn(remotePacketConn)
	var done atomic.Bool
	go m.packetConnectionCopy(ctx, conn, destination, false, &done, onClose)
	go m.packetConnectionCopy(ctx, destination, conn, true, &done, onClose)
}

func (m *ConnectionManager) tryAcquireConnection() error {
	if !m.processFDBudgetAvailable() {
		return errProcessFDBudget
	}
	select {
	case m.connectionSlots <- struct{}{}:
		return nil
	default:
		return errConnectionCapacity
	}
}

func (m *ConnectionManager) manageConnectionClose(onClose N.CloseHandlerFunc) N.CloseHandlerFunc {
	var releaseOnce sync.Once
	return func(err error) {
		releaseOnce.Do(func() {
			<-m.connectionSlots
			if onClose != nil {
				onClose(err)
			}
		})
	}
}

func (m *ConnectionManager) processFDBudgetAvailable() bool {
	blocked := m.processFDBlocked.Load()
	attempt := m.connectionAttempts.Add(1)
	if !blocked && attempt%processFDCheckEveryAttempts != 0 {
		return true
	}
	now := time.Now().UnixNano()
	lastCheckAt := m.lastProcessFDCheckAtNanos.Load()
	if now-lastCheckAt < processFDCheckMinInterval.Nanoseconds() ||
		!m.lastProcessFDCheckAtNanos.CompareAndSwap(lastCheckAt, now) {
		return !blocked
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return !blocked
	}
	fdCount := len(entries)
	m.lastProcessFDCount.Store(int64(fdCount))
	blocked = fdCount >= processConnectionFDStopCount ||
		(blocked && fdCount >= processConnectionFDResumeCount)
	m.processFDBlocked.Store(blocked)
	return !blocked
}

func (m *ConnectionManager) logConnectionRejection(
	ctx context.Context,
	this N.Dialer,
	network string,
	metadata adapter.InboundContext,
	reason error,
) {
	rejected := m.connectionRejected.Add(1)
	if rejected&(rejected-1) != 0 {
		return
	}
	var outboundTag string
	if outbound, isOutbound := this.(adapter.Outbound); isOutbound {
		outboundTag = outbound.Tag()
	}
	var (
		processUID int32
		packages   string
	)
	if metadata.ProcessInfo != nil {
		processUID = metadata.ProcessInfo.UserId
		packages = strings.Join(metadata.ProcessInfo.AndroidPackageNames, ",")
	}
	m.logger.WarnContext(
		ctx,
		"pre-handshake connection rejected: reason=", reason,
		" network=", network,
		" source=", metadata.Source,
		" destination=", metadata.Destination,
		" protocol=", metadata.Protocol,
		" uid=", processUID,
		" packages=", packages,
		" route_outbound=", metadata.Outbound,
		" outbound=", outboundTag,
		" active=", len(m.connectionSlots),
		" limit=", cap(m.connectionSlots),
		" process_fd=", m.lastProcessFDCount.Load(),
		" rejected=", rejected,
	)
}

func (m *ConnectionManager) logPhysicalBudgetSnapshot(ctx context.Context) {
	now := time.Now().UnixNano()
	lastLogAt := m.lastPhysicalBudgetLogNanos.Load()
	if now-lastLogAt < time.Second.Nanoseconds() ||
		!m.lastPhysicalBudgetLogNanos.CompareAndSwap(lastLogAt, now) {
		return
	}
	snapshot := dialer.CurrentPhysicalBudgetSnapshot()
	m.logger.InfoContext(
		ctx,
		"kunbox_physical_budget_v1 ", snapshot.String(),
		" pending_sources=", snapshot.PendingSources(),
	)
}

func newPacketDialContext(parent context.Context, conn N.PacketConn) (context.Context, context.CancelFunc) {
	timeoutConn, loaded := common.Cast[canceler.PacketConn](conn)
	if !loaded {
		return parent, func() {}
	}
	dialContext, cancel := context.WithCancel(parent)
	if timeoutConn.Timeout() <= 0 {
		cancel()
		return dialContext, cancel
	}
	go func() {
		// Poll only during a pending dial; remove when UDPNAT exposes close notification.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-dialContext.Done():
				return
			case <-ticker.C:
				if timeoutConn.Timeout() <= 0 {
					cancel()
					return
				}
			}
		}
	}()
	return dialContext, cancel
}

func (m *ConnectionManager) connectionCopy(ctx context.Context, source net.Conn, destination net.Conn, direction bool, done *atomic.Bool, onClose N.CloseHandlerFunc) {
	_, err := bufio.CopyWithIncreateBuffer(destination, source, bufio.DefaultIncreaseBufferAfter, bufio.DefaultBatchSize)
	if err != nil {
		common.Close(source, destination)
	} else {
		destinationWriter, _ := N.UnwrapCountWriter(destination, nil)
		duplexDst, isDuplex := N.UnwrapWriter(destinationWriter).(N.WriteCloser)
		if isDuplex {
			err = duplexDst.CloseWrite()
			if err != nil {
				common.Close(source, destination)
			}
		} else {
			destination.Close()
		}
	}
	if done.Swap(true) {
		if onClose != nil {
			onClose(err)
		}
		common.Close(source, destination)
	}
	if !direction {
		if err == nil {
			m.logger.DebugContext(ctx, "connection upload finished")
		} else if !E.IsClosedOrCanceled(err) {
			m.logger.ErrorContext(ctx, "connection upload closed: ", err)
		} else {
			m.logger.TraceContext(ctx, "connection upload closed")
		}
	} else {
		if err == nil {
			m.logger.DebugContext(ctx, "connection download finished")
		} else if !E.IsClosedOrCanceled(err) {
			m.logger.ErrorContext(ctx, "connection download closed: ", err)
		} else {
			m.logger.TraceContext(ctx, "connection download closed")
		}
	}
}

func (m *ConnectionManager) kickWriteHandshake(ctx context.Context, source net.Conn, destination net.Conn, serverFirst bool, direction bool, done *atomic.Bool, onClose N.CloseHandlerFunc) bool {
	if !N.NeedHandshakeForWrite(destination) {
		return false
	}
	var (
		err          error
		wrotePayload bool
	)
	if serverFirst {
		_ = destination.SetWriteDeadline(time.Now().Add(C.ReadPayloadTimeout))
		_, err = destination.Write(nil)
		_ = destination.SetWriteDeadline(time.Time{})
	} else {
		var cachedBuffer *buf.Buffer
		sourceReader, readCounters := N.UnwrapCountReader(source, nil)
		destinationWriter, writeCounters := N.UnwrapCountWriter(destination, nil)
		if cachedReader, ok := sourceReader.(N.CachedReader); ok {
			cachedBuffer = cachedReader.ReadCached()
		}
		if cachedBuffer != nil {
			wrotePayload = true
			dataLen := cachedBuffer.Len()
			_, err = destinationWriter.Write(cachedBuffer.Bytes())
			cachedBuffer.Release()
			if err == nil {
				for _, counter := range readCounters {
					counter(int64(dataLen))
				}
				for _, counter := range writeCounters {
					counter(int64(dataLen))
				}
			}
		} else {
			_ = destination.SetWriteDeadline(time.Now().Add(C.ReadPayloadTimeout))
			_, err = destinationWriter.Write(nil)
			_ = destination.SetWriteDeadline(time.Time{})
		}
	}
	if err == nil {
		return false
	}
	if !wrotePayload && (E.IsMulti(err, os.ErrInvalid, context.DeadlineExceeded, io.EOF) || E.IsTimeout(err)) {
		return false
	}
	if !done.Swap(true) {
		if onClose != nil {
			onClose(err)
		}
	}
	common.Close(source, destination)
	if !direction {
		m.logger.ErrorContext(ctx, "connection upload handshake: ", err)
	} else {
		m.logger.ErrorContext(ctx, "connection download handshake: ", err)
	}
	return true
}

func (m *ConnectionManager) packetConnectionCopy(ctx context.Context, source N.PacketReader, destination N.PacketWriter, direction bool, done *atomic.Bool, onClose N.CloseHandlerFunc) {
	_, err := bufio.CopyPacket(destination, source)
	if !direction {
		if err == nil {
			m.logger.DebugContext(ctx, "packet upload finished")
		} else if E.IsClosedOrCanceled(err) {
			m.logger.TraceContext(ctx, "packet upload closed")
		} else {
			m.logger.DebugContext(ctx, "packet upload closed: ", err)
		}
	} else {
		if err == nil {
			m.logger.DebugContext(ctx, "packet download finished")
		} else if E.IsClosedOrCanceled(err) {
			m.logger.TraceContext(ctx, "packet download closed")
		} else {
			m.logger.DebugContext(ctx, "packet download closed: ", err)
		}
	}
	if !done.Swap(true) {
		if onClose != nil {
			onClose(err)
		}
	}
	common.Close(source, destination)
}

type trackedConn struct {
	net.Conn
	manager *ConnectionManager
	element *list.Element[io.Closer]
}

func (c *trackedConn) Close() error {
	c.manager.access.Lock()
	c.manager.connections.Remove(c.element)
	c.manager.access.Unlock()
	return c.Conn.Close()
}

func (c *trackedConn) Upstream() any {
	return c.Conn
}

func (c *trackedConn) ReaderReplaceable() bool {
	return true
}

func (c *trackedConn) WriterReplaceable() bool {
	return true
}

type trackedPacketConn struct {
	net.PacketConn
	manager *ConnectionManager
	element *list.Element[io.Closer]
}

func (c *trackedPacketConn) Close() error {
	c.manager.access.Lock()
	c.manager.connections.Remove(c.element)
	c.manager.access.Unlock()
	return c.PacketConn.Close()
}

func (c *trackedPacketConn) Upstream() any {
	return bufio.NewPacketConn(c.PacketConn)
}

func (c *trackedPacketConn) ReaderReplaceable() bool {
	return true
}

func (c *trackedPacketConn) WriterReplaceable() bool {
	return true
}
