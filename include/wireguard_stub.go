//go:build !with_wireguard

package include

import (
	"context"

	"github.com/roseforljh/RayBox-rx/adapter"
	"github.com/roseforljh/RayBox-rx/adapter/endpoint"
	C "github.com/roseforljh/RayBox-rx/constant"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.WireGuardEndpointOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardEndpointOptions) (adapter.Endpoint, error) {
		return nil, E.New(`WireGuard is not included in this build, rebuild with -tags with_wireguard`)
	})
}
