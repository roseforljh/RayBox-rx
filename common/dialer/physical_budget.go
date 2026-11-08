package dialer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/roseforljh/RayBox-rx/adapter"
)

const (
	physicalDialGateMarker       = "kunbox_physical_dial_gate_v1"
	maxPendingPhysicalDials      = 512
	maxActivePhysicalSockets     = 4096
	maxPhysicalFDStopCount       = 8192
	maxPhysicalFDResumeCount     = 4096
	physicalFDCheckEveryAttempts = 32
	physicalFDCheckMinInterval   = 250 * time.Millisecond
	physicalDialMaximumDuration  = 15 * time.Second
	minimumActivePhysicalSockets = 32
	minimumPendingPhysicalDials  = 16
	minimumPhysicalFDReserve     = 64
)

var errPhysicalLeaseReleased = errors.New("physical dial lease already released")

type physicalFDLimit struct {
	softLimit uint64
	infinite  bool
}

type physicalBudgetLimits struct {
	pendingLimit  int
	activeLimit   int
	fdStopLine    int
	fdResumeLine  int
	blockedReason string
}

func resolvePhysicalBudgetLimits(limit physicalFDLimit, readErr error) physicalBudgetLimits {
	if readErr != nil {
		return physicalBudgetLimits{blockedReason: "rlimit_unavailable"}
	}
	if limit.infinite || limit.softLimit == math.MaxUint64 {
		return physicalBudgetLimits{
			pendingLimit: maxPendingPhysicalDials,
			activeLimit:  maxActivePhysicalSockets,
			fdStopLine:   maxPhysicalFDStopCount,
			fdResumeLine: maxPhysicalFDResumeCount,
		}
	}
	reserve := limit.softLimit / 4
	if reserve < minimumPhysicalFDReserve {
		reserve = minimumPhysicalFDReserve
	}
	if limit.softLimit <= reserve+minimumActivePhysicalSockets {
		return physicalBudgetLimits{blockedReason: "rlimit_too_low"}
	}
	stopLine := minUint64(maxPhysicalFDStopCount, limit.softLimit-reserve)
	resumeLine := minUint64(maxPhysicalFDResumeCount, stopLine/2)
	activeLimit := minUint64(maxActivePhysicalSockets, stopLine/2)
	pendingLimit := activeLimit / 8
	if pendingLimit < minimumPendingPhysicalDials {
		pendingLimit = minimumPendingPhysicalDials
	}
	pendingLimit = minUint64(maxPendingPhysicalDials, pendingLimit)
	if stopLine <= resumeLine || activeLimit < minimumActivePhysicalSockets ||
		pendingLimit < minimumPendingPhysicalDials {
		return physicalBudgetLimits{blockedReason: "rlimit_budget_invalid"}
	}
	return physicalBudgetLimits{
		pendingLimit: int(pendingLimit),
		activeLimit:  int(activeLimit),
		fdStopLine:   int(stopLine),
		fdResumeLine: int(resumeLine),
	}
}

func minUint64(limit int, value uint64) uint64 {
	if value > uint64(limit) {
		return uint64(limit)
	}
	return value
}

type physicalAttempt struct {
	cancel context.CancelFunc
	source string
}

type physicalSocketBudget struct {
	limits  physicalBudgetLimits
	pending chan struct{}
	active  chan struct{}

	attempts         atomic.Uint64
	rejected         atomic.Uint64
	nextID           atomic.Uint64
	fdCount          atomic.Int64
	fdBlocked        atomic.Bool
	lastFDCheckNanos atomic.Int64

	attemptAccess   sync.Mutex
	pendingAttempts map[uint64]physicalAttempt
	fdCountReader   func() (int, bool, error)
}

func newPhysicalSocketBudget(limits physicalBudgetLimits) *physicalSocketBudget {
	pendingLimit := max(1, limits.pendingLimit)
	activeLimit := max(1, limits.activeLimit)
	budget := &physicalSocketBudget{
		limits:          limits,
		pending:         make(chan struct{}, pendingLimit),
		active:          make(chan struct{}, activeLimit),
		pendingAttempts: make(map[uint64]physicalAttempt),
		fdCountReader:   readPhysicalProcessFDCount,
	}
	budget.fdCount.Store(-1)
	if limits.blockedReason != "" {
		budget.fdBlocked.Store(true)
	}
	return budget
}

func newGlobalPhysicalSocketBudget() *physicalSocketBudget {
	limit, err := readPhysicalFDSoftLimit()
	return newPhysicalSocketBudget(resolvePhysicalBudgetLimits(limit, err))
}

var globalPhysicalSocketBudget = newGlobalPhysicalSocketBudget()

type PhysicalBudgetSnapshot struct {
	Pending         int
	Active          int
	Attempts        uint64
	Rejected        uint64
	FDCount         int64
	Blocked         bool
	BlockedReason   string
	PendingLimit    int
	ActiveLimit     int
	FDStopLine      int
	FDResumeLine    int
	PendingBySource map[string]int
}

func CurrentPhysicalBudgetSnapshot() PhysicalBudgetSnapshot {
	return globalPhysicalSocketBudget.snapshot()
}

func (b *physicalSocketBudget) snapshot() PhysicalBudgetSnapshot {
	b.attemptAccess.Lock()
	pendingBySource := make(map[string]int)
	for _, attempt := range b.pendingAttempts {
		pendingBySource[attempt.source]++
	}
	b.attemptAccess.Unlock()
	return PhysicalBudgetSnapshot{
		Pending:         len(b.pending),
		Active:          len(b.active),
		Attempts:        b.attempts.Load(),
		Rejected:        b.rejected.Load(),
		FDCount:         b.fdCount.Load(),
		Blocked:         b.fdBlocked.Load(),
		BlockedReason:   b.limits.blockedReason,
		PendingLimit:    b.limits.pendingLimit,
		ActiveLimit:     b.limits.activeLimit,
		FDStopLine:      b.limits.fdStopLine,
		FDResumeLine:    b.limits.fdResumeLine,
		PendingBySource: pendingBySource,
	}
}

func (s PhysicalBudgetSnapshot) String() string {
	return fmt.Sprintf(
		"pending=%d active=%d attempts=%d rejected=%d fd=%d blocked=%t "+
			"pending_limit=%d active_limit=%d fd_stop_line=%d fd_resume_line=%d",
		s.Pending,
		s.Active,
		s.Attempts,
		s.Rejected,
		s.FDCount,
		s.Blocked,
		s.PendingLimit,
		s.ActiveLimit,
		s.FDStopLine,
		s.FDResumeLine,
	)
}

func (s PhysicalBudgetSnapshot) PendingSources() string {
	if len(s.PendingBySource) == 0 {
		return ""
	}
	type sourceCount struct {
		source string
		count  int
	}
	counts := make([]sourceCount, 0, len(s.PendingBySource))
	for source, count := range s.PendingBySource {
		counts = append(counts, sourceCount{source: source, count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].count == counts[j].count {
			return counts[i].source < counts[j].source
		}
		return counts[i].count > counts[j].count
	})
	if len(counts) > 8 {
		counts = counts[:8]
	}
	parts := make([]string, 0, len(counts))
	for _, count := range counts {
		parts = append(parts, fmt.Sprintf("%s:%d", count.source, count.count))
	}
	return strings.Join(parts, ",")
}

func (b *physicalSocketBudget) reject(reason string) error {
	b.rejected.Add(1)
	return fmt.Errorf("%s: %s %s", physicalDialGateMarker, reason, b.snapshot().String())
}

func (b *physicalSocketBudget) checkCapacity() error {
	b.attempts.Add(1)
	if b.limits.blockedReason != "" {
		return b.reject(b.limits.blockedReason)
	}
	blocked := b.fdBlocked.Load()
	attempt := b.attempts.Load()
	if !blocked && attempt%physicalFDCheckEveryAttempts != 0 {
		return nil
	}
	now := time.Now().UnixNano()
	lastCheck := b.lastFDCheckNanos.Load()
	if now-lastCheck < physicalFDCheckMinInterval.Nanoseconds() ||
		!b.lastFDCheckNanos.CompareAndSwap(lastCheck, now) {
		if blocked {
			return b.reject("fd_budget_blocked")
		}
		return nil
	}
	fdCount, supported, err := b.fdCountReader()
	if err != nil {
		b.fdBlocked.Store(true)
		b.cancelPendingAttempts()
		return b.reject("fd_count_unavailable")
	}
	if !supported {
		return nil
	}
	b.fdCount.Store(int64(fdCount))
	nextBlocked := fdCount >= b.limits.fdStopLine || blocked && fdCount >= b.limits.fdResumeLine
	b.fdBlocked.Store(nextBlocked)
	if nextBlocked {
		b.cancelPendingAttempts()
		return b.reject("fd_budget_blocked")
	}
	return nil
}

func (b *physicalSocketBudget) cancelPendingAttempts() {
	b.attemptAccess.Lock()
	cancelFunctions := make([]context.CancelFunc, 0, len(b.pendingAttempts))
	for _, attempt := range b.pendingAttempts {
		cancelFunctions = append(cancelFunctions, attempt.cancel)
	}
	b.attemptAccess.Unlock()
	for _, cancel := range cancelFunctions {
		cancel()
	}
}

func physicalAttemptSource(ctx context.Context) string {
	metadata := adapter.ContextFrom(ctx)
	if metadata == nil {
		return "unknown"
	}
	parts := make([]string, 0, 4)
	if metadata.ProcessInfo != nil {
		parts = append(parts, fmt.Sprintf("uid=%d", metadata.ProcessInfo.UserId))
		if len(metadata.ProcessInfo.AndroidPackageNames) > 0 {
			packages := append([]string(nil), metadata.ProcessInfo.AndroidPackageNames...)
			sort.Strings(packages)
			parts = append(parts, "pkg="+strings.Join(packages, "|"))
		}
	}
	if metadata.Inbound != "" {
		parts = append(parts, "in="+metadata.Inbound)
	}
	if metadata.Outbound != "" {
		parts = append(parts, "out="+metadata.Outbound)
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ";")
}

type PhysicalDialLease struct {
	budget  *physicalSocketBudget
	id      uint64
	cancel  context.CancelFunc
	access  sync.Mutex
	pending bool
}

func AcquirePhysicalDial(ctx context.Context) (context.Context, *PhysicalDialLease, error) {
	return globalPhysicalSocketBudget.acquirePending(ctx)
}

func (b *physicalSocketBudget) acquirePending(ctx context.Context) (context.Context, *PhysicalDialLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := b.checkCapacity(); err != nil {
		return nil, nil, err
	}
	select {
	case b.pending <- struct{}{}:
	default:
		return nil, nil, b.reject("pending_capacity_reached")
	}
	dialContext, cancel := context.WithTimeout(ctx, physicalDialMaximumDuration)
	id := b.nextID.Add(1)
	lease := &PhysicalDialLease{
		budget:  b,
		id:      id,
		cancel:  cancel,
		pending: true,
	}
	b.attemptAccess.Lock()
	b.pendingAttempts[id] = physicalAttempt{cancel: cancel, source: physicalAttemptSource(ctx)}
	b.attemptAccess.Unlock()
	return dialContext, lease, nil
}

func (l *PhysicalDialLease) Promote() (*PhysicalSocketLease, error) {
	l.access.Lock()
	defer l.access.Unlock()
	if !l.pending {
		return nil, errPhysicalLeaseReleased
	}
	select {
	case l.budget.active <- struct{}{}:
		l.releasePendingLocked()
		return &PhysicalSocketLease{budget: l.budget}, nil
	default:
		l.releasePendingLocked()
		return nil, l.budget.reject("active_capacity_reached")
	}
}

func (l *PhysicalDialLease) Release() {
	l.access.Lock()
	defer l.access.Unlock()
	if l.pending {
		l.releasePendingLocked()
	}
}

func (l *PhysicalDialLease) releasePendingLocked() {
	l.budget.attemptAccess.Lock()
	delete(l.budget.pendingAttempts, l.id)
	l.budget.attemptAccess.Unlock()
	l.cancel()
	<-l.budget.pending
	l.pending = false
}

type PhysicalSocketLease struct {
	budget *physicalSocketBudget
	once   sync.Once
}

func (l *PhysicalSocketLease) Release() {
	if l == nil || l.budget == nil {
		return
	}
	l.once.Do(func() { <-l.budget.active })
}

func ReservePhysicalSockets(count int) ([]*PhysicalSocketLease, error) {
	return globalPhysicalSocketBudget.reserveActive(count)
}

func ReservePhysicalSocket() (func(), error) {
	leases, err := ReservePhysicalSockets(1)
	if err != nil {
		return nil, err
	}
	return leases[0].Release, nil
}

func (b *physicalSocketBudget) reserveActive(count int) ([]*PhysicalSocketLease, error) {
	if count <= 0 {
		return nil, nil
	}
	if err := b.checkCapacity(); err != nil {
		return nil, err
	}
	leases := make([]*PhysicalSocketLease, 0, count)
	for range count {
		select {
		case b.active <- struct{}{}:
			leases = append(leases, &PhysicalSocketLease{budget: b})
		default:
			for _, lease := range leases {
				lease.Release()
			}
			return nil, b.reject("active_capacity_reached")
		}
	}
	return leases, nil
}

func dialPhysicalConn(ctx context.Context, dial func(context.Context) (net.Conn, error)) (net.Conn, error) {
	return dialPhysicalConnWithBudget(globalPhysicalSocketBudget, ctx, dial)
}

func dialPhysicalConnWithBudget(
	budget *physicalSocketBudget,
	ctx context.Context,
	dial func(context.Context) (net.Conn, error),
) (net.Conn, error) {
	dialContext, pendingLease, err := budget.acquirePending(ctx)
	if err != nil {
		return nil, err
	}
	conn, dialErr := dial(dialContext)
	if dialErr != nil {
		if conn != nil {
			conn.Close()
		}
		pendingLease.Release()
		return nil, dialErr
	}
	activeLease, err := pendingLease.Promote()
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &budgetedConn{Conn: conn, lease: activeLease}, nil
}

func listenPhysicalPacket(
	ctx context.Context,
	listen func(context.Context) (net.PacketConn, error),
) (net.PacketConn, error) {
	return listenPhysicalPacketWithBudget(globalPhysicalSocketBudget, ctx, listen)
}

func listenPhysicalPacketWithBudget(
	budget *physicalSocketBudget,
	ctx context.Context,
	listen func(context.Context) (net.PacketConn, error),
) (net.PacketConn, error) {
	dialContext, pendingLease, err := budget.acquirePending(ctx)
	if err != nil {
		return nil, err
	}
	conn, listenErr := listen(dialContext)
	if listenErr != nil {
		if conn != nil {
			conn.Close()
		}
		pendingLease.Release()
		return nil, listenErr
	}
	activeLease, err := pendingLease.Promote()
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &budgetedPacketConn{PacketConn: conn, lease: activeLease}, nil
}

type budgetedConn struct {
	net.Conn
	lease *PhysicalSocketLease
}

func (c *budgetedConn) Close() error {
	err := c.Conn.Close()
	c.lease.Release()
	return err
}

func (c *budgetedConn) Upstream() any {
	return c.Conn
}

type budgetedPacketConn struct {
	net.PacketConn
	lease *PhysicalSocketLease
}

func (c *budgetedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.lease.Release()
	return err
}

func (c *budgetedPacketConn) Upstream() any {
	return c.PacketConn
}
