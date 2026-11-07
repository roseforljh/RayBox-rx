//go:build !with_dhcp

package include

import (
	"context"

	"github.com/roseforljh/RayBox-rx/adapter"
	C "github.com/roseforljh/RayBox-rx/constant"
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerDHCPTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.DHCPDNSServerOptions](registry, C.DNSTypeDHCP, func(ctx context.Context, logger log.ContextLogger, tag string, options option.DHCPDNSServerOptions) (adapter.DNSTransport, error) {
		return nil, E.New(`DHCP is not included in this build, rebuild with -tags with_dhcp`)
	})
}
