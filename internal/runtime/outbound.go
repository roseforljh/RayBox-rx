package runtime

import (
	"context"
	gonet "net"
	"sync"

	"github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/errors"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/session"
	"github.com/roseforljh/RayBox-rx/internal/protocol/core"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/grpc"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/splithttp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/trojan"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/httpupgrade"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tagged/taggedimpl"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tcp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/udp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/websocket"
)

// Outbound is the minimum protocol service needed by a RayBox outbound.
type Outbound struct {
	Handler      proxy.Outbound
	Service      *core.Service
	SystemDialer internet.SystemDialer
	Stream       *internet.MemoryStreamConfig
	OwnerContext context.Context
	mu           sync.Mutex
	closed       bool
	physical     map[*physicalConn]struct{}
}

// Dialer adapts the owner protected dialer to Xray's internet dialer.
type Dialer struct {
	Owner   N.Dialer
	Stream  *internet.MemoryStreamConfig
	Runtime *Outbound
}

type ownerSystemDialer struct{ dialer Dialer }

func (d ownerSystemDialer) Dial(ctx context.Context, _ net.Address, destination net.Destination, _ *internet.SocketConfig) (gonet.Conn, error) {
	conn, err := d.dialer.Owner.DialContext(ctx, destination.Network.SystemString(), M.ParseSocksaddrHostPort(destination.Address.String(), uint16(destination.Port)))
	if err != nil {
		return nil, err
	}
	if d.dialer.Runtime != nil {
		conn, err = d.dialer.Runtime.trackPhysical(conn)
		if err != nil {
			return nil, err
		}
	}
	if destination.Network == net.Network_UDP {
		return &net.PacketConnWrapper{PacketConn: bufio.NewUnbindPacketConn(conn), Dest: conn.RemoteAddr()}, nil
	}
	return conn, nil
}

func (ownerSystemDialer) DestIpAddress() net.IP { return nil }

func (d Dialer) Dial(ctx context.Context, destination net.Destination) (stat.Connection, error) {
	if d.Owner == nil {
		return nil, errors.New("missing protected owner dialer")
	}
	ctx = internet.ContextWithSystemDialer(ctx, ownerSystemDialer{dialer: d})
	return internet.Dial(ctx, destination, d.Stream)
}

func (d Dialer) DestIpAddress() net.IP { return nil }

func (d Dialer) SetOutboundGateway(context.Context, *session.Outbound) {}

// NewConfig converts a single protocol outbound config into a standalone handler.
func NewConfig(ctx context.Context, config *core.OutboundHandlerConfig, service *core.Service) (*Outbound, error) {
	if config == nil || config.ProxySettings == nil {
		return nil, errors.New("missing protocol proxy settings")
	}
	if service == nil || service.Policy == nil {
		return nil, errors.New("missing standalone protocol policy")
	}
	ownerContext := ctx
	var stream *internet.MemoryStreamConfig
	if config.SenderSettings != nil {
		sender, err := config.SenderSettings.GetInstance()
		if err != nil {
			return nil, err
		}
		settings, ok := sender.(*proxyman.SenderConfig)
		if !ok {
			return nil, errors.New("invalid sender settings")
		}
		stream, err = internet.ToMemoryStreamConfig(settings.StreamSettings)
		if err != nil {
			return nil, err
		}
		ctx = session.ContextWithStreamSettings(ctx, stream)
	}
	ctx = core.ContextWithService(ctx, service)
	ctx = core.ContextWithCone(ctx, true)
	proxyConfig, err := config.ProxySettings.GetInstance()
	if err != nil {
		return nil, err
	}
	handler, err := common.CreateObject(ctx, proxyConfig)
	if err != nil {
		return nil, err
	}
	protocolHandler, ok := handler.(proxy.Outbound)
	if !ok {
		return nil, errors.New("protocol config is not an outbound handler")
	}
	return &Outbound{Handler: protocolHandler, Service: service, Stream: stream, OwnerContext: ownerContext}, nil
}

// Process forwards one request through the protocol handler and the owner dialer.
func (o *Outbound) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	ctx = core.ContextWithService(ctx, o.Service)
	if o.SystemDialer != nil {
		ctx = internet.ContextWithSystemDialer(ctx, o.SystemDialer)
	}
	return o.Handler.Process(ctx, link, dialer)
}

type physicalConn struct {
	gonet.Conn
	owner *Outbound
	once  sync.Once
	err   error
}

func (c *physicalConn) NetConn() gonet.Conn { return c.Conn }

func (c *physicalConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.physical, c)
		c.owner.mu.Unlock()
	})
	return c.err
}

func (o *Outbound) trackPhysical(conn gonet.Conn) (gonet.Conn, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		conn.Close()
		return nil, gonet.ErrClosed
	}
	if o.physical == nil {
		o.physical = make(map[*physicalConn]struct{})
	}
	tracked := &physicalConn{Conn: conn, owner: o}
	o.physical[tracked] = struct{}{}
	return tracked, nil
}

// Close releases handler-owned protocol resources.
func (o *Outbound) Close() error {
	o.mu.Lock()
	o.closed = true
	connections := make([]*physicalConn, 0, len(o.physical))
	for conn := range o.physical {
		connections = append(connections, conn)
	}
	o.mu.Unlock()
	for _, conn := range connections {
		conn.Close()
	}
	splithttp.CloseStream(o.Stream)
	grpc.CloseStream(o.Stream)
	if closer, ok := o.Handler.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
