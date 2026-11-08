//go:build with_tailscale

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/certificate"
	"github.com/roseforljh/RayBox-rx/adapter/endpoint"
	"github.com/roseforljh/RayBox-rx/adapter/service"
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/protocol/tailscale"
	"github.com/roseforljh/RayBox-rx/service/derp"
)

func registerTailscaleEndpoint(registry *endpoint.Registry) {
	tailscale.RegisterEndpoint(registry)
}

func registerTailscaleTransport(registry *dns.TransportRegistry) {
	tailscale.RegistryTransport(registry)
}

func registerTailscaleCertificateProvider(registry *certificate.Registry) {
	tailscale.RegisterCertificateProvider(registry)
}

func registerDERPService(registry *service.Registry) {
	derp.Register(registry)
}
