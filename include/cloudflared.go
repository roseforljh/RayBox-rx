//go:build with_cloudflared

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/inbound"
	"github.com/roseforljh/RayBox-rx/protocol/cloudflare"
)

func registerCloudflaredInbound(registry *inbound.Registry) {
	cloudflare.RegisterInbound(registry)
}
