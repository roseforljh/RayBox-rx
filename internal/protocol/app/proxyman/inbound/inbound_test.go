package inbound

import (
	"context"
	"errors"
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/serial"
)

type failingHandler struct {
	tag                string
	startErr, closeErr error
	closed             int
}

func (h *failingHandler) Tag() string                          { return h.tag }
func (h *failingHandler) Start() error                         { return h.startErr }
func (h *failingHandler) Close() error                         { h.closed++; return h.closeErr }
func (*failingHandler) ReceiverSettings() *serial.TypedMessage { return nil }
func (*failingHandler) ProxySettings() *serial.TypedMessage    { return nil }

func TestAddHandlerStartFailure(t *testing.T) {
	for _, tag := range []string{"tagged", ""} {
		for _, closeErr := range []error{nil, errors.New("close failed")} {
			t.Run(tag+"/"+fmtError(closeErr), func(t *testing.T) {
				ctx := context.Background()
				m, _ := New(ctx, nil)
				if err := m.Start(); err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				good := &failingHandler{tag: "existing"}
				if err := m.AddHandler(ctx, good); err != nil {
					t.Fatal(err)
				}
				startErr := errors.New("start failed")
				bad := &failingHandler{tag: tag, startErr: startErr, closeErr: closeErr}
				if err := m.AddHandler(ctx, bad); err != startErr {
					t.Fatalf("lost start error: %v", err)
				}
				if bad.closed != 1 {
					t.Fatalf("Close called %d times", bad.closed)
				}
				if got := m.ListHandlers(ctx); len(got) != 1 || got[0] != good {
					t.Fatal("failed handler retained or existing handler removed")
				}
				if err := m.AddHandler(ctx, &failingHandler{tag: tag}); err != nil {
					t.Fatalf("retry failed: %v", err)
				}
			})
		}
	}
}

func fmtError(err error) string {
	if err == nil {
		return "close-ok"
	}
	return "close-error"
}
