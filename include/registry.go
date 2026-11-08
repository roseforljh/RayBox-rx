package include

import (
	"context"

	box "github.com/roseforljh/RayBox-rx"
	"github.com/roseforljh/RayBox-rx/adapter"
	"github.com/roseforljh/RayBox-rx/adapter/certificate"
	"github.com/roseforljh/RayBox-rx/adapter/endpoint"
	"github.com/roseforljh/RayBox-rx/adapter/inbound"
	"github.com/roseforljh/RayBox-rx/adapter/outbound"
	"github.com/roseforljh/RayBox-rx/adapter/service"
	C "github.com/roseforljh/RayBox-rx/constant"
	"github.com/roseforljh/RayBox-rx/dns"
	"github.com/roseforljh/RayBox-rx/dns/transport"
	"github.com/roseforljh/RayBox-rx/dns/transport/fakeip"
	"github.com/roseforljh/RayBox-rx/dns/transport/hosts"
	"github.com/roseforljh/RayBox-rx/dns/transport/local"
	"github.com/roseforljh/RayBox-rx/dns/transport/mdns"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/roseforljh/RayBox-rx/protocol/anytls"
	"github.com/roseforljh/RayBox-rx/protocol/block"
	"github.com/roseforljh/RayBox-rx/protocol/bridge"
	"github.com/roseforljh/RayBox-rx/protocol/direct"
	"github.com/roseforljh/RayBox-rx/protocol/group"
	"github.com/roseforljh/RayBox-rx/protocol/http"
	"github.com/roseforljh/RayBox-rx/protocol/mixed"
	"github.com/roseforljh/RayBox-rx/protocol/naive"
	"github.com/roseforljh/RayBox-rx/protocol/redirect"
	"github.com/roseforljh/RayBox-rx/protocol/session"
	"github.com/roseforljh/RayBox-rx/protocol/shadowsocks"
	"github.com/roseforljh/RayBox-rx/protocol/shadowtls"
	"github.com/roseforljh/RayBox-rx/protocol/snell"
	"github.com/roseforljh/RayBox-rx/protocol/socks"
	"github.com/roseforljh/RayBox-rx/protocol/ssh"
	"github.com/roseforljh/RayBox-rx/protocol/tor"
	"github.com/roseforljh/RayBox-rx/protocol/trojan"
	"github.com/roseforljh/RayBox-rx/protocol/tun"
	"github.com/roseforljh/RayBox-rx/protocol/vless"
	"github.com/roseforljh/RayBox-rx/protocol/vmess"
	"github.com/roseforljh/RayBox-rx/service/api"
	originca "github.com/roseforljh/RayBox-rx/service/origin_ca"
	"github.com/roseforljh/RayBox-rx/service/resolved"
	"github.com/roseforljh/RayBox-rx/service/ssmapi"
	E "github.com/sagernet/sing/common/exceptions"
)

func Context(ctx context.Context) context.Context {
	return box.Context(ctx, InboundRegistry(), OutboundRegistry(), EndpointRegistry(), DNSTransportRegistry(), ServiceRegistry(), CertificateProviderRegistry())
}

func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()

	tun.RegisterInbound(registry)
	redirect.RegisterRedirect(registry)
	redirect.RegisterTProxy(registry)
	direct.RegisterInbound(registry)

	socks.RegisterInbound(registry)
	http.RegisterInbound(registry)
	mixed.RegisterInbound(registry)

	shadowsocks.RegisterInbound(registry)
	snell.RegisterInbound(registry)
	vmess.RegisterInbound(registry)
	trojan.RegisterInbound(registry)
	naive.RegisterInbound(registry)
	shadowtls.RegisterInbound(registry)
	vless.RegisterInbound(registry)
	anytls.RegisterInbound(registry)

	registerQUICInbounds(registry)
	registerCloudflaredInbound(registry)
	registerStubForRemovedInbounds(registry)

	return registry
}

func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()

	direct.RegisterOutbound(registry)
	bridge.RegisterOutbound(registry)

	block.RegisterOutbound(registry)

	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)

	socks.RegisterOutbound(registry)
	http.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	snell.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	registerNaiveOutbound(registry)
	tor.RegisterOutbound(registry)
	ssh.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)
	anytls.RegisterOutbound(registry)
	session.RegisterOutbound(registry)

	registerQUICOutbounds(registry)
	registerStubForRemovedOutbounds(registry)

	return registry
}

func EndpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()

	registerWireGuardEndpoint(registry)
	registerOpenConnectEndpoint(registry)
	registerOpenVPNEndpoints(registry)
	registerTailscaleEndpoint(registry)

	return registry
}

func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()

	transport.RegisterTCP(registry)
	transport.RegisterUDP(registry)
	transport.RegisterTLS(registry)
	transport.RegisterHTTPS(registry)
	hosts.RegisterTransport(registry)
	local.RegisterTransport(registry)
	mdns.RegisterTransport(registry)
	fakeip.RegisterTransport(registry)
	resolved.RegisterTransport(registry)

	registerQUICTransports(registry)
	registerDHCPTransport(registry)
	registerTailscaleTransport(registry)
	registerOpenConnectDNSTransport(registry)
	registerOpenVPNDNSTransport(registry)

	return registry
}

func ServiceRegistry() *service.Registry {
	registry := service.NewRegistry()

	api.RegisterService(registry)
	resolved.RegisterService(registry)
	ssmapi.RegisterService(registry)

	registerQUICServices(registry)
	registerDERPService(registry)
	registerCCMService(registry)
	registerOCMService(registry)
	registerOOMKillerService(registry)
	registerUSBIPServices(registry)

	return registry
}

func CertificateProviderRegistry() *certificate.Registry {
	registry := certificate.NewRegistry()

	registerACMECertificateProvider(registry)
	registerTailscaleCertificateProvider(registry)
	originca.RegisterCertificateProvider(registry)

	return registry
}

func registerStubForRemovedInbounds(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
}

func registerStubForRemovedOutbounds(registry *outbound.Registry) {
	outbound.Register[option.ShadowsocksROutboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksROutboundOptions) (adapter.Outbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
	outbound.Register[option.StubOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Outbound, error) {
		return nil, E.New("WireGuard outbound is deprecated in sing-box 1.11.0 and removed in sing-box 1.13.0, use WireGuard endpoint instead")
	})
}
