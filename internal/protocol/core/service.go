package core

import (
	"context"

	"github.com/roseforljh/RayBox-rx/internal/protocol/features/policy"
	"github.com/roseforljh/RayBox-rx/internal/protocol/features/routing"
)

// Service contains the small feature surface required by a standalone protocol handler.
type Service struct {
	Policy         policy.Manager
	Routing        routing.Dispatcher
	Standalone     bool
	PacketEncoding *string
}

type serviceKey struct{}

// ContextWithService attaches a standalone protocol service to ctx.
func ContextWithService(ctx context.Context, service *Service) context.Context {
	return context.WithValue(ctx, serviceKey{}, service)
}

// ServiceFromContext returns a standalone protocol service, if present.
func ServiceFromContext(ctx context.Context) *Service {
	service, _ := ctx.Value(serviceKey{}).(*Service)
	return service
}

// PolicyManager returns the service policy or the legacy instance policy.
func PolicyManager(ctx context.Context) policy.Manager {
	if service := ServiceFromContext(ctx); service != nil && service.Policy != nil {
		return service.Policy
	}
	instance := MustFromContext(ctx)
	return instance.GetFeature(policy.ManagerType()).(policy.Manager)
}

// ContextWithCone preserves the legacy transport feature flag without an instance.
func ContextWithCone(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, coneKey{}, enabled)
}

type coneKey struct{}

// ConeFromContext returns the standalone cone setting.
func ConeFromContext(ctx context.Context) (bool, bool) {
	if value, ok := ctx.Value(coneKey{}).(bool); ok {
		return value, true
	}
	value, ok := ctx.Value("cone").(bool)
	return value, ok
}

// PacketEncodingFromContext returns the explicit VLESS packet encoding, if configured.
func PacketEncodingFromContext(ctx context.Context) (*string, bool) {
	service := ServiceFromContext(ctx)
	if service == nil {
		return nil, false
	}
	return service.PacketEncoding, service.PacketEncoding != nil
}
