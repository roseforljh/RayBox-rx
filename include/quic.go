//go:build with_quic

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/inbound"
	"github.com/roseforljh/RayBox-rx/adapter/outbound"
	"github.com/roseforljh/RayBox-rx/adapter/service"
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/dns/transport/quic"
	"github.com/roseforljh/RayBox-rx/protocol/hysteria"
	"github.com/roseforljh/RayBox-rx/protocol/hysteria2"
	_ "github.com/roseforljh/RayBox-rx/protocol/naive/quic"
	"github.com/roseforljh/RayBox-rx/protocol/tuic"
	_ "github.com/roseforljh/RayBox-rx/transport/v2rayquic"
)

func registerQUICInbounds(registry *inbound.Registry) {
	hysteria.RegisterInbound(registry)
	tuic.RegisterInbound(registry)
	hysteria2.RegisterInbound(registry)
}

func registerQUICOutbounds(registry *outbound.Registry) {
	hysteria.RegisterOutbound(registry)
	tuic.RegisterOutbound(registry)
	hysteria2.RegisterOutbound(registry)
}

func registerQUICTransports(registry *dns.TransportRegistry) {
	quic.RegisterTransport(registry)
	quic.RegisterHTTP3Transport(registry)
}

func registerQUICServices(registry *service.Registry) {
	hysteria2.RegisterRealmService(registry)
}
