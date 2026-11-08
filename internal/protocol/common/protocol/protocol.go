package protocol // import "github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol"

import (
	"errors"
)

var ErrProtoNeedMoreData = errors.New("protocol matches, but need more data to complete sniffing")
