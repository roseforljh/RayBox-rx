package runtime

import (
	"context"
	"net"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common"
	xnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net/cnc"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/session"
	"github.com/roseforljh/RayBox-rx/internal/protocol/core"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/pipe"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// Dial opens one protocol connection without constructing a core.Instance.
func (o *Outbound) Dial(ctx context.Context, network string, destination M.Socksaddr, dialer internet.Dialer) (net.Conn, error) {
	if o == nil || o.Handler == nil {
		return nil, net.ErrClosed
	}
	xnetwork := xnet.Network_TCP
	if N.NetworkName(network) == N.NetworkUDP {
		xnetwork = xnet.Network_UDP
	} else if N.NetworkName(network) != N.NetworkTCP {
		return nil, N.ErrUnknownNetwork
	}
	target := xnet.Destination{Address: xnet.ParseAddress(destination.AddrString()), Port: xnet.Port(destination.Port), Network: xnetwork}
	reader, writer := pipe.New()
	outReader, outWriter := pipe.New()
	ctx, cancel := context.WithCancel(ctx)
	requestCtx := session.ContextWithOutbounds(core.ContextWithService(ctx, o.Service), []*session.Outbound{{Target: target}})
	requestCtx = internet.ContextWithOwner(requestCtx, o.OwnerContext)
	requestCtx = session.ContextWithStreamSettings(requestCtx, o.Stream)
	if owner, ok := dialer.(Dialer); ok {
		owner.Stream = o.Stream
		owner.Runtime = o
		dialer = owner
	}
	go func() {
		defer cancel()
		err := o.Process(requestCtx, &transport.Link{Reader: reader, Writer: outWriter}, dialer)
		if err != nil {
			common.Interrupt(outWriter)
		} else {
			common.Close(outWriter)
		}
		common.Close(reader)
	}()
	var output cnc.ConnectionOption
	if xnetwork == xnet.Network_UDP {
		output = cnc.ConnectionOutputMultiUDP(outReader)
	} else {
		output = cnc.ConnectionOutputMulti(outReader)
	}
	return cnc.NewConnection(cnc.ConnectionInputMulti(writer), output, cnc.ConnectionOnClose(common.ChainedClosable{cancelCloser(cancel), writer, outWriter})), nil
}

type cancelCloser context.CancelFunc

func (c cancelCloser) Close() error { c(); return nil }

var _ internet.Dialer = Dialer{}
