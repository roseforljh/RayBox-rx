package settings

import (
	"context"

	"github.com/roseforljh/RayBox-rx/adapter"
)

type WIFIMonitor interface {
	ReadWIFIState(ctx context.Context) adapter.WIFIState
	Start() error
	Close() error
}
