//go:build !with_openvpn

package include

import (
	"context"

	"github.com/roseforljh/RayBox-rx/adapter"
	"github.com/roseforljh/RayBox-rx/adapter/endpoint"
	C "github.com/roseforljh/RayBox-rx/constant"
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	endpoint.Register[option.OpenVPNClientEndpointOptions](registry, C.TypeOpenVPNClient, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenVPNClientEndpointOptions) (adapter.Endpoint, error) {
		if !options.System {
			return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn,with_gvisor for system:false`)
		}
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
	endpoint.Register[option.OpenVPNServerEndpointOptions](registry, C.TypeOpenVPNServer, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenVPNServerEndpointOptions) (adapter.Endpoint, error) {
		if !options.System {
			return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn,with_gvisor for system:false`)
		}
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.OpenVPNDNSServerOptions](registry, C.DNSTypeOpenVPN, func(ctx context.Context, logger log.ContextLogger, tag string, options option.OpenVPNDNSServerOptions) (adapter.DNSTransport, error) {
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
}
