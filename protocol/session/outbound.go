// SimpleNet embeds Xray in the same Go runtime as libbox. There is no second
// Android VPN, public listener, or unprotected Xray system socket.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/roseforljh/RayBox-rx/adapter"
	"github.com/roseforljh/RayBox-rx/adapter/outbound"
	"github.com/roseforljh/RayBox-rx/common/dialer"
	"github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman"
	xbuf "github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	xnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/core"
	"github.com/roseforljh/RayBox-rx/internal/protocol/features/policy"
	"github.com/roseforljh/RayBox-rx/internal/protocol/infra/conf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	standalone "github.com/roseforljh/RayBox-rx/internal/runtime"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/bufio/deadline"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/dispatcher"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/log"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/policy"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/inbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/trojan"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/grpc"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/httpupgrade"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/splithttp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tagged/taggedimpl"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tcp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/udp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/websocket"
)

type Options struct {
	option.DialerOptions
	Outbound       json.RawMessage    `json:"outbound"`
	Protocol       string             `json:"-"`
	Network        option.NetworkList `json:"-"`
	PacketEncoding *string            `json:"-"`
}

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[Options](registry, "xray", NewOutbound)
}

type Outbound struct {
	outbound.Adapter
	ctx            context.Context
	dialer         N.Dialer
	standalone     *standalone.Outbound
	packetEncoding *string
	access         sync.Mutex
	closed         bool
	connections    map[*managedConn]struct{}
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

func outboundRemoteIsDomain(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var findAddress func(json.RawMessage) string
	findAddress = func(data json.RawMessage) string {
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil {
			return ""
		}
		if address, ok := object["address"]; ok {
			var value string
			if json.Unmarshal(address, &value) == nil {
				return value
			}
		}
		for _, key := range []string{"settings", "vnext", "servers"} {
			if nested, ok := object[key]; ok {
				var list []json.RawMessage
				if json.Unmarshal(nested, &list) == nil && len(list) > 0 {
					if address := findAddress(list[0]); address != "" {
						return address
					}
				}
				if address := findAddress(nested); address != "" {
					return address
				}
			}
		}
		return ""
	}
	address := findAddress(json.RawMessage(value["settings"]))
	if address == "" {
		address = findAddress(raw)
	}
	return net.ParseIP(strings.Trim(address, "[]")) == nil && address != ""
}

func NewOutbound(ctx context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	config, err := buildConfig(options.Outbound)
	if err != nil {
		return nil, fmt.Errorf("invalid Xray outbound: %w", err)
	}
	if err := applyOOBDialerOptions(config, &options.DialerOptions); err != nil {
		return nil, err
	}
	protectedDialer, err := dialer.New(ctx, options.DialerOptions, outboundRemoteIsDomain(options.Outbound))
	if err != nil {
		return nil, err
	}
	service := &core.Service{Policy: policy.DefaultManager{}, Standalone: true, PacketEncoding: options.PacketEncoding}
	standaloneOutbound, err := standalone.NewConfig(ctx, config.Outbound[0], service)
	if err != nil {
		return nil, err
	}
	outboundType := options.Protocol
	if outboundType == "" {
		outboundType = "xray"
	}
	return &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(outboundType, tag, options.Network.Build(), options.DialerOptions),
		ctx:     ctx, dialer: protectedDialer, standalone: standaloneOutbound, packetEncoding: options.PacketEncoding,
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
	o.ctx = scope.Context()
	scope.Add(func() error {
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
		return o.standalone.Close()
	})
	return nil
}

func (o *Outbound) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	conn, err := o.standaloneDial(ctx, network, dest)
	if err != nil {
		return nil, err
	}
	if N.NetworkName(network) == N.NetworkUDP {
		packet := newPacketConn(conn, dest)
		packet.fixed = o.packetEncoding != nil && *o.packetEncoding == ""
		return bufio.NewBindPacketConn(packet, dest), nil
	}
	return &deadlineConn{ExtendedConn: deadline.NewConn(conn)}, nil
}

// The protocol pipe has no write deadline support. Combined deadlines still
// set the wrapper's read deadline, but must not report a write deadline as set.
type deadlineConn struct {
	N.ExtendedConn
}

func (c *deadlineConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *deadlineConn) SetWriteDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	return fmt.Errorf("session write deadline: %w", errors.ErrUnsupported)
}

func (o *Outbound) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	conn, err := o.standaloneDial(ctx, N.NetworkUDP, dest)
	if err != nil {
		return nil, err
	}
	packet := newPacketConn(conn, dest)
	packet.fixed = o.packetEncoding != nil && *o.packetEncoding == ""
	return deadline.NewPacketConn(bufio.NewPacketConn(packet)), nil
}

func (o *Outbound) standaloneDial(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	conn, err := o.standalone.Dial(ctx, network, dest, standalone.Dialer{Owner: o.dialer})
	if err != nil {
		return nil, err
	}
	return o.track(conn)
}

func (o *Outbound) track(conn net.Conn) (net.Conn, error) {
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

// Protocol connection helpers split writes into 8 KiB chunks and expose no real
// deadlines. Use its packet buffers directly to preserve UDP boundaries, large
// packets and per-packet destinations; sing's wrapper supplies read deadlines.
type packetConn struct {
	net.Conn
	destination M.Socksaddr
	fixed       bool
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
	if c.fixed && dest != c.destination {
		return 0, fmt.Errorf("UDP destination differs from fixed target")
	}
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
