package core

import (
	"context"
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/features/policy"
)

func TestStandaloneServicePolicyDoesNotRequireInstance(t *testing.T) {
	service := &Service{Policy: policy.DefaultManager{}, Standalone: true}
	ctx := ContextWithService(context.Background(), service)
	if got := PolicyManager(ctx); got == nil {
		t.Fatal("standalone policy is nil")
	}
	if !service.Standalone {
		t.Fatal("standalone service flag was lost")
	}
}
