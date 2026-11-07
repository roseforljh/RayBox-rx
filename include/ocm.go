//go:build with_ocm

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/service"
	"github.com/roseforljh/RayBox-rx/service/ocm"
)

func registerOCMService(registry *service.Registry) {
	ocm.RegisterService(registry)
}
