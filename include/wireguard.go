//go:build with_wireguard

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/endpoint"
	"github.com/roseforljh/RayBox-rx/protocol/wireguard"
)

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterEndpoint(registry)
}
