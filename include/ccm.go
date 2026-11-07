//go:build with_ccm && (!darwin || cgo)

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/service"
	"github.com/roseforljh/RayBox-rx/service/ccm"
)

func registerCCMService(registry *service.Registry) {
	ccm.RegisterService(registry)
}
