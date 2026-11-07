package extension

import (
	"context"

	"github.com/roseforljh/RayBox-rx/internal/protocol/features"
	"google.golang.org/protobuf/proto"
)

type Observatory interface {
	features.Feature

	GetObservation(ctx context.Context) (proto.Message, error)
}

type BurstObservatory interface {
	Observatory
	Check(tag []string)
}

func ObservatoryType() interface{} {
	return (*Observatory)(nil)
}
