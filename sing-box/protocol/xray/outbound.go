// SimpleNet embeds Xray in the same Go runtime as libbox. There is no second
// Android VPN, public listener, or unprotected Xray system socket.
package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/bufio/deadline"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/app/proxyman"
	xbuf "github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"

	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/policy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/proxy/trojan"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"
	_ "github.com/xtls/xray-core/proxy/vmess/outbound"
	_ "github.com/xtls/xray-core/transport/internet/grpc"
	_ "github.com/xtls/xray-core/transport/internet/httpupgrade"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tagged/taggedimpl"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/udp"
	_ "github.com/xtls/xray-core/transport/internet/websocket"
)

type Options struct {
	option.DialerOptions
	Outbound json.RawMessage `json:"outbound"`
}

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, "xray", NewOutbound)
}

// Set once at process initialization, never during reload. Select the sing-box
// dialer by Xray instance, including when Xray detaches a request's context.
var instanceDialers sync.Map

func init() {
	internet.UseAlternativeSystemDialer(systemDialer{})
}

type systemDialer struct{}

func (systemDialer) Dial(ctx context.Context, _ xnet.Address, dest xnet.Destination, _ *internet.SocketConfig) (net.Conn, error) {
	value, ok := instanceDialers.Load(core.FromContext(ctx))
	if !ok {
		return nil, fmt.Errorf("Xray instance has no protected sing-box dialer")
	}
	conn, err := value.(N.Dialer).DialContext(ctx, dest.Network.SystemString(), M.ParseSocksaddrHostPort(dest.Address.String(), uint16(dest.Port)))
	if err != nil {
		return nil, err
	}
	if dest.Network == xnet.Network_UDP {
		if _, ok := conn.(*xnet.PacketConnWrapper); !ok {
			// Xray's QUIC transports require its packet wrapper. Keep sing-box's
			// protected, tracked connection instead of unwrapping its socket.
			return &xnet.PacketConnWrapper{PacketConn: bufio.NewUnbindPacketConn(conn), Dest: conn.RemoteAddr()}, nil
		}
	}
	return conn, nil
}

func (systemDialer) DestIpAddress() xnet.IP { return nil }

type Outbound struct {
	outbound.Adapter
	ctx         context.Context
	dialer      N.Dialer
	config      *core.Config
	instance    *core.Instance
	access      sync.Mutex
	closed      bool
	connections map[*managedConn]struct{}
}

// Only one proxy outbound is accepted: routing, DNS and socket policy remain
// under sing-box control. Native socket overrides would bypass that contract.
func buildConfig(raw json.RawMessage) (*core.Config, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	for key := range value {
		switch key {
		case "protocol", "settings", "streamSettings", "tag":
		default:
			return nil, fmt.Errorf("unsupported Xray outbound field: %s", key)
		}
	}
	var protocol string
	if err := json.Unmarshal(value["protocol"], &protocol); err != nil {
		return nil, err
	}
	if protocol != "vless" && protocol != "vmess" && protocol != "trojan" {
		return nil, fmt.Errorf("unsupported Xray protocol: %s", protocol)
	}
	if stream, ok := value["streamSettings"]; ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(stream, &fields); err != nil {
			return nil, err
		}
		if _, ok := fields["sockopt"]; ok {
			return nil, fmt.Errorf("Xray sockopt is managed by sing-box")
		}
	}
	var proxy conf.OutboundDetourConfig
	if err := json.Unmarshal(raw, &proxy); err != nil {
		return nil, err
	}
	config := conf.Config{OutboundConfigs: []conf.OutboundDetourConfig{proxy}, LogConfig: &conf.LogConfig{LogLevel: "none"}}
	return config.Build()
}

func NewOutbound(ctx context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	config, err := buildConfig(options.Outbound)
	if err != nil {
		return nil, fmt.Errorf("invalid Xray outbound: %w", err)
	}
	if err := applyOOBDialerOptions(config, &options.DialerOptions); err != nil {
		return nil, err
	}
	protectedDialer, err := dialer.New(ctx, options.DialerOptions, true)
	if err != nil {
		return nil, err
	}
	return &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions("xray", tag, []string{N.NetworkTCP, N.NetworkUDP}, options.DialerOptions),
		ctx:     ctx, dialer: protectedDialer, config: config,
	}, nil
}

func applyOOBDialerOptions(config *core.Config, options *option.DialerOptions) error {
	sender, err := config.Outbound[0].SenderSettings.GetInstance()
	if err != nil {
		return err
	}
	stream := sender.(*proxyman.SenderConfig).StreamSettings
	if stream == nil || stream.GetEffectiveProtocol() != "splithttp" {
		return nil
	}
	settings, err := stream.GetEffectiveTransportSettings()
	if err != nil {
		return err
	}
	o, err := settings.(*splithttp.Config).NormalizeOOB()
	if err != nil || o == nil {
		return err
	}
	if options.Detour != "" || options.TCPFastOpen {
		return fmt.Errorf("XHTTP OOB requires native protected TCP without detour or TCP fast open")
	}
	if o.FallbackSettings.GetSocketSettings() != nil {
		return fmt.Errorf("OOB fallback sockopt is managed by sing-box")
	}
	if o.TcpMaxSeg < 0 {
		return nil
	}
	previous := options.TCPControl
	options.TCPControl = func(network, address string, raw syscall.RawConn) error {
		if previous != nil {
			if err := previous(network, address, raw); err != nil {
				return err
			}
		}
		if !strings.HasPrefix(network, "tcp") {
			return nil
		}
		var socketErr error
		if err := raw.Control(func(fd uintptr) { socketErr = internet.SetTCPMaxSegment(fd, int(o.TcpMaxSeg)) }); err != nil {
			return err
		}
		return socketErr
	}
	return nil
}

func (o *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	instance, err := core.NewWithContext(scope.Context(), o.config)
	if err != nil {
		return err
	}
	o.instance = instance
	o.ctx = scope.Context()
	instanceDialers.Store(instance, o.dialer)
	scope.Add(func() error {
		// Close workers before removing their dialer; a new instance has its own
		// entry so shutting down the old profile cannot affect a reloaded one.
		o.access.Lock()
		o.closed = true
		connections := make([]*managedConn, 0, len(o.connections))
		for conn := range o.connections {
			connections = append(connections, conn)
		}
		o.access.Unlock()
		for _, conn := range connections {
			conn.Close()
		}
		err := instance.Close()
		instanceDialers.Delete(instance)
		return err
	})
	return instance.Start()
}

func (o *Outbound) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if o.instance == nil {
		return nil, fmt.Errorf("Xray outbound is not started")
	}
	var target xnet.Destination
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		target = xnet.TCPDestination(xnet.ParseAddress(dest.AddrString()), xnet.Port(dest.Port))
	case N.NetworkUDP:
		target = xnet.UDPDestination(xnet.ParseAddress(dest.AddrString()), xnet.Port(dest.Port))
	default:
		return nil, fmt.Errorf("unsupported Xray network: %s", network)
	}
	conn, err := o.dial(ctx, target)
	if err != nil {
		return nil, err
	}
	if N.NetworkName(network) == N.NetworkUDP {
		return bufio.NewBindPacketConn(newPacketConn(conn, dest), dest), nil
	}
	return deadline.NewConn(conn), nil
}

func (o *Outbound) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	if o.instance == nil {
		return nil, fmt.Errorf("Xray outbound is not started")
	}
	conn, err := o.dial(ctx, xnet.UDPDestination(xnet.ParseAddress(dest.AddrString()), xnet.Port(dest.Port)))
	if err != nil {
		return nil, err
	}
	return deadline.NewPacketConn(bufio.NewPacketConn(newPacketConn(conn, dest))), nil
}

func (o *Outbound) dial(ctx context.Context, target xnet.Destination) (net.Conn, error) {
	conn, err := core.Dial(ctx, o.instance, target)
	if err != nil {
		return nil, err
	}
	managed := &managedConn{Conn: conn, owner: o}
	o.access.Lock()
	if o.closed {
		o.access.Unlock()
		conn.Close()
		return nil, net.ErrClosed
	}
	if o.connections == nil {
		o.connections = make(map[*managedConn]struct{})
	}
	o.connections[managed] = struct{}{}
	o.access.Unlock()
	return managed, nil
}

type managedConn struct {
	net.Conn
	owner *Outbound
	once  sync.Once
	err   error
}

func (c *managedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.access.Lock()
		delete(c.owner.connections, c)
		c.owner.access.Unlock()
	})
	return c.err
}

func (c *managedConn) ReadMultiBuffer() (xbuf.MultiBuffer, error) {
	return c.Conn.(xbuf.Reader).ReadMultiBuffer()
}
func (c *managedConn) WriteMultiBuffer(buffers xbuf.MultiBuffer) error {
	return c.Conn.(xbuf.Writer).WriteMultiBuffer(buffers)
}

// core.Dial's net.Conn helpers split writes into 8 KiB chunks and expose no real
// deadlines. Use its packet buffers directly to preserve UDP boundaries, large
// packets and per-packet destinations; sing's wrapper supplies read deadlines.
type packetConn struct {
	net.Conn
	destination M.Socksaddr
	reader      xbuf.Reader
	writer      xbuf.Writer
	readAccess  sync.Mutex
	pending     xbuf.MultiBuffer
}

func newPacketConn(conn net.Conn, destination M.Socksaddr) *packetConn {
	return &packetConn{Conn: conn, destination: destination, reader: conn.(xbuf.Reader), writer: conn.(xbuf.Writer)}
}

func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	for len(c.pending) == 0 {
		packets, err := c.reader.ReadMultiBuffer()
		if err != nil {
			return 0, nil, err
		}
		c.pending = packets
	}
	packet := c.pending[0]
	c.pending[0] = nil
	c.pending = c.pending[1:]
	defer packet.Release()
	destination := c.destination
	if packet.UDP != nil {
		destination = M.ParseSocksaddrHostPort(packet.UDP.Address.String(), uint16(packet.UDP.Port))
	}
	return copy(p, packet.Bytes()), destination, nil
}

func (c *packetConn) WriteTo(p []byte, address net.Addr) (int, error) {
	// Xray's XUDP writer reserves 666 bytes in each 8 KiB buffer. It
	// silently discards larger datagrams; surface an error instead.
	if len(p) > xbuf.Size-666 {
		return 0, fmt.Errorf("UDP packet exceeds Xray XUDP limit (%d bytes)", xbuf.Size-666)
	}
	dest := M.SocksaddrFromNet(address)
	packet := xbuf.NewWithSize(int32(len(p)))
	if _, err := packet.Write(p); err != nil {
		packet.Release()
		return 0, err
	}
	target := xnet.UDPDestination(xnet.ParseAddress(dest.AddrString()), xnet.Port(dest.Port))
	packet.UDP = &target
	if err := c.writer.WriteMultiBuffer(xbuf.MultiBuffer{packet}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *packetConn) Close() error {
	err := c.Conn.Close()
	c.readAccess.Lock()
	xbuf.ReleaseMulti(c.pending)
	c.pending = nil
	c.readAccess.Unlock()
	return err
}
