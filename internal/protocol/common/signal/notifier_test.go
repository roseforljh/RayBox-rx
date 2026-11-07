package signal_test

import (
	"testing"

	. "github.com/roseforljh/RayBox-rx/internal/protocol/common/signal"
)

func TestNotifierSignal(t *testing.T) {
	n := NewNotifier()

	w := n.Wait()
	n.Signal()

	select {
	case <-w:
	default:
		t.Fail()
	}
}
