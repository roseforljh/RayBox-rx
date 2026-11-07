//go:build with_naive_outbound

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/outbound"
	"github.com/roseforljh/RayBox-rx/protocol/naive"
)

func registerNaiveOutbound(registry *outbound.Registry) {
	naive.RegisterOutbound(registry)
}
