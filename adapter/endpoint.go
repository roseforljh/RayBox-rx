package adapter

import (
	"context"

	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
)

type Endpoint interface {
	Lifecycle
	Type() string
	Tag() string
	Outbound
}

type EndpointRegistry interface {
	option.EndpointOptionsRegistry
	Create(ctx context.Context, router Router, logger log.ContextLogger, tag string, endpointType string, options any) (Endpoint, error)
}

type EndpointManager interface {
	Lifecycle
	StartEndpoint(endpoint Endpoint) error
	Endpoints() []Endpoint
	Get(tag string) (Endpoint, bool)
	Create(ctx context.Context, router Router, logger log.ContextLogger, tag string, endpointType string, options any) error
}
