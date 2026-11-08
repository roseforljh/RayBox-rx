package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/service"
	"github.com/roseforljh/RayBox-rx/service/oomkiller"
)

func registerOOMKillerService(registry *service.Registry) {
	oomkiller.RegisterService(registry)
}
