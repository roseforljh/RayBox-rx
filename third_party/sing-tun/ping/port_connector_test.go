package ping

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
)

func TestPortUsesDestinationConnector(t *testing.T) {
	want := errors.New("connector called")
	var got netip.Addr
	port := NewPortWithDestinationConnector(context.Background(), logger.NOP(), nil, time.Minute,
		func(_ context.Context, _ logger.ContextLogger, _ control.Func, destination netip.Addr, _ PacketWriter, _ time.Duration) (*Destination, error) {
			got = destination
			return nil, want
		})
	destination := netip.MustParseAddr("192.0.2.1")
	_, err := port.flowFor(netip.MustParseAddr("192.0.2.2"), destination, 42)
	if !errors.Is(err, want) || got != destination {
		t.Fatalf("connector destination=%v error=%v", got, err)
	}
}
