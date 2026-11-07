//go:build with_openvpn

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/endpoint"
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/protocol/openvpn"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	openvpn.RegisterEndpoint(registry)
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	openvpn.RegisterDNSTransport(registry)
}
