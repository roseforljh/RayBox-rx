package udp

import (
	"github.com/roseforljh/RayBox-rx/internal/protocol/common"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
)

func init() {
	common.Must(internet.RegisterProtocolConfigCreator(protocolName, func() interface{} {
		return new(Config)
	}))
}
