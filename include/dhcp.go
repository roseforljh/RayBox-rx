//go:build with_dhcp

package include

import (
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/dns/transport/dhcp"
)

func registerDHCPTransport(registry *dns.TransportRegistry) {
	dhcp.RegisterTransport(registry)
}
