package dialer

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolvePhysicalBudgetLimits(t *testing.T) {
	tests := []struct {
		name     string
		limit    physicalFDLimit
		readErr  error
		blocked  bool
		stopLine int
		active   int
		pending  int
	}{
		{name: "read failure", readErr: errors.New("failed"), blocked: true},
		{name: "64", limit: physicalFDLimit{softLimit: 64}, blocked: true},
		{name: "96", limit: physicalFDLimit{softLimit: 96}, blocked: true},
		{name: "128", limit: physicalFDLimit{softLimit: 128}, stopLine: 64, active: 32, pending: 16},
		{name: "256", limit: physicalFDLimit{softLimit: 256}, stopLine: 192, active: 96, pending: 16},
		{name: "1024", limit: physicalFDLimit{softLimit: 1024}, stopLine: 768, active: 384, pending: 48},
		{name: "32768", limit: physicalFDLimit{softLimit: 32768}, stopLine: 8192, active: 4096, pending: 512},
		{name: "infinity", limit: physicalFDLimit{infinite: true}, stopLine: 8192, active: 4096, pending: 512},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := resolvePhysicalBudgetLimits(test.limit, test.readErr)
			if (limits.blockedReason != "") != test.blocked {
				t.Fatalf("blocked=%t reason=%q", limits.blockedReason != "", limits.blockedReason)
			}
			if test.blocked {
				return
			}
			if limits.fdStopLine != test.stopLine || limits.activeLimit != test.active ||
				limits.pendingLimit != test.pending {
				t.Fatalf("unexpected limits: %+v", limits)
			}
			if uint64(limits.fdStopLine) > test.limit.softLimit && !test.limit.infinite {
				t.Fatalf("stop line exceeds soft limit: %+v", limits)
			}
		})
	}
}

func TestPhysicalPendingAndActiveLifecycle(t *testing.T) {
	budget := newTestPhysicalBudget(2, 2)
	ctx1, pending1, err := budget.acquirePending(context.Background())
	if err != nil || ctx1 == nil {
		t.Fatal(err)
	}
	_, pending2, err := budget.acquirePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = budget.acquirePending(context.Background()); err == nil {
		t.Fatal("expected pending capacity rejection")
	}
	active1, err := pending1.Promote()
	if err != nil {
		t.Fatal(err)
	}
	pending2.Release()
	if snapshot := budget.snapshot(); snapshot.Pending != 0 || snapshot.Active != 1 {
		t.Fatalf("unexpected promoted snapshot: %+v", snapshot)
	}
	active1.Release()
	active1.Release()
	if snapshot := budget.snapshot(); snapshot.Pending != 0 || snapshot.Active != 0 {
		t.Fatalf("leases did not return to zero: %+v", snapshot)
	}
}

func TestPhysicalFDBreakerCancelsPendingAndRecovers(t *testing.T) {
	budget := newTestPhysicalBudget(4, 4)
	var fdCount atomic.Int64
	fdCount.Store(1)
	budget.fdCountReader = func() (int, bool, error) {
		return int(fdCount.Load()), true, nil
	}
	dialContext, lease, err := budget.acquirePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fdCount.Store(int64(budget.limits.fdStopLine))
	budget.attempts.Store(physicalFDCheckEveryAttempts - 1)
	if err = budget.checkCapacity(); err == nil {
		t.Fatal("expected fd breaker rejection")
	}
	select {
	case <-dialContext.Done():
	case <-time.After(time.Second):
		t.Fatal("pending dial was not cancelled")
	}
	lease.Release()
	fdCount.Store(int64(budget.limits.fdResumeLine - 1))
	budget.lastFDCheckNanos.Store(time.Now().Add(-physicalFDCheckMinInterval - time.Millisecond).UnixNano())
	if err = budget.checkCapacity(); err != nil {
		t.Fatalf("fd breaker did not recover: %v", err)
	}
}

func TestDialPhysicalConnClosesPartialError(t *testing.T) {
	budget := newTestPhysicalBudget(2, 2)
	conn := &testPhysicalConn{}
	result, err := dialPhysicalConnWithBudget(
		budget,
		context.Background(),
		func(context.Context) (net.Conn, error) {
			return conn, errors.New("partial dial")
		},
	)
	if err == nil || result != nil {
		t.Fatal("expected dial error")
	}
	if !conn.closed.Load() {
		t.Fatal("partial connection was not closed")
	}
	if snapshot := budget.snapshot(); snapshot.Pending != 0 || snapshot.Active != 0 {
		t.Fatalf("partial error leaked lease: %+v", snapshot)
	}
}

func TestRealLoopbackPhysicalConnectionsStayBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 64)
	var acceptWaitGroup sync.WaitGroup
	acceptWaitGroup.Add(1)
	go func() {
		defer acceptWaitGroup.Done()
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepted <- conn
		}
	}()

	budget := newTestPhysicalBudget(16, 8)
	var (
		waitGroup sync.WaitGroup
		access    sync.Mutex
		clients   []net.Conn
	)
	for range 64 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			conn, dialErr := dialPhysicalConnWithBudget(
				budget,
				context.Background(),
				func(ctx context.Context) (net.Conn, error) {
					var netDialer net.Dialer
					return netDialer.DialContext(ctx, "tcp", listener.Addr().String())
				},
			)
			if dialErr == nil {
				access.Lock()
				clients = append(clients, conn)
				access.Unlock()
			}
		}()
	}
	waitGroup.Wait()
	if snapshot := budget.snapshot(); snapshot.Active > 8 {
		t.Fatalf("active sockets exceeded budget: %+v", snapshot)
	}
	for _, conn := range clients {
		conn.Close()
	}
	listener.Close()
	acceptWaitGroup.Wait()
	close(accepted)
	for conn := range accepted {
		conn.Close()
	}
	if snapshot := budget.snapshot(); snapshot.Pending != 0 || snapshot.Active != 0 {
		t.Fatalf("real sockets did not return to zero: %+v", snapshot)
	}
}

func newTestPhysicalBudget(pendingLimit, activeLimit int) *physicalSocketBudget {
	return newPhysicalSocketBudget(physicalBudgetLimits{
		pendingLimit: pendingLimit,
		activeLimit:  activeLimit,
		fdStopLine:   8192,
		fdResumeLine: 4096,
	})
}

type testPhysicalConn struct {
	closed atomic.Bool
}

func (*testPhysicalConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (*testPhysicalConn) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (c *testPhysicalConn) Close() error                   { c.closed.Store(true); return nil }
func (*testPhysicalConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*testPhysicalConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*testPhysicalConn) SetDeadline(time.Time) error      { return nil }
func (*testPhysicalConn) SetReadDeadline(time.Time) error  { return nil }
func (*testPhysicalConn) SetWriteDeadline(time.Time) error { return nil }
