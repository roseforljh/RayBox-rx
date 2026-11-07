package direct

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/roseforljh/RayBox-rx/common/dialer"
)

func TestAcquireBudgetedPingDestinationLifecycle(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	destination := &testPingDestination{}
	wrapped, err := acquireBudgetedPingDestination(
		context.Background(),
		func(context.Context) (*testPingDestination, error) {
			if snapshot := dialer.CurrentPhysicalBudgetSnapshot(); snapshot.Pending <= before.Pending {
				t.Fatal("ICMP socket creation was not preceded by a pending lease")
			}
			return destination, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := dialer.CurrentPhysicalBudgetSnapshot(); snapshot.Active <= before.Active {
		t.Fatal("successful ICMP destination did not hold an active lease")
	}
	_ = wrapped.Close()
	_ = wrapped.Close()
	after := dialer.CurrentPhysicalBudgetSnapshot()
	if after.Pending != before.Pending || after.Active != before.Active {
		t.Fatalf("ICMP lease leaked: before=%+v after=%+v", before, after)
	}
}

func TestAcquireBudgetedPingDestinationFailureReleasesPending(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	_, err := acquireBudgetedPingDestination(
		context.Background(),
		func(context.Context) (*testPingDestination, error) {
			return nil, errors.New("connect failed")
		},
	)
	if err == nil {
		t.Fatal("expected connect failure")
	}
	after := dialer.CurrentPhysicalBudgetSnapshot()
	if after.Pending != before.Pending || after.Active != before.Active {
		t.Fatalf("failed ICMP connect leaked lease: before=%+v after=%+v", before, after)
	}
}

func TestAcquireBudgetedPingDestinationReleasesAlreadyClosedDestination(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	destination := &testPingDestination{}
	wrapped, err := acquireBudgetedPingDestination(
		context.Background(),
		func(context.Context) (*testPingDestination, error) {
			_ = destination.Close()
			return destination, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != destination {
		t.Fatal("unexpected destination")
	}
	after := dialer.CurrentPhysicalBudgetSnapshot()
	if after.Pending != before.Pending || after.Active != before.Active {
		t.Fatalf("already closed ICMP destination leaked lease: before=%+v after=%+v", before, after)
	}
}

func TestAcquireBudgetedPingDestinationCancelledContext(t *testing.T) {
	before := dialer.CurrentPhysicalBudgetSnapshot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := acquireBudgetedPingDestination(ctx, func(context.Context) (*testPingDestination, error) {
		t.Fatal("connector ran after cancellation")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	after := dialer.CurrentPhysicalBudgetSnapshot()
	if after.Pending != before.Pending || after.Active != before.Active {
		t.Fatalf("cancelled ICMP connect leaked lease: before=%+v after=%+v", before, after)
	}
}

type testPingDestination struct {
	access    sync.Mutex
	closed    bool
	callbacks []func()
}

func (d *testPingDestination) AddCloseCallback(callback func()) {
	d.access.Lock()
	if !d.closed {
		d.callbacks = append(d.callbacks, callback)
		d.access.Unlock()
		return
	}
	d.access.Unlock()
	callback()
}

func (d *testPingDestination) Close() error {
	d.access.Lock()
	if d.closed {
		d.access.Unlock()
		return nil
	}
	d.closed = true
	callbacks := d.callbacks
	d.callbacks = nil
	d.access.Unlock()
	for _, callback := range callbacks {
		callback()
	}
	return nil
}
