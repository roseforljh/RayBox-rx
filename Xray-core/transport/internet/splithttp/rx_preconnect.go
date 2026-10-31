package splithttp

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/transport/internet/reality"
	"golang.org/x/net/http2"
)

type rxWarmConn struct {
	conn net.Conn
	at   time.Time
}

// Only the downlink uses this pool. A single filler bounds queued + background
// handshakes by K. An empty-pool cold dial belongs to the one active GET, outside
// that standby budget. No HTTP bytes are sent before checkout.
type rxPreconnect struct {
	dial      func(context.Context) (net.Conn, error)
	version   string
	opts      *RXConfig
	mu        sync.Mutex
	queue     []rxWarmConn
	wake      chan struct{}
	cancel    context.CancelFunc
	closed    bool
	warmHits  uint64
	coldDials uint64
}

func (p *rxPreconnect) start(ctx context.Context) {
	p.mu.Lock()
	if p.closed || p.cancel != nil {
		p.mu.Unlock()
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	p.wake = make(chan struct{}, 1)
	p.mu.Unlock()
	context.AfterFunc(ctx, p.close)
	if p.opts.Preconnects != 0 {
		go p.fill(ctx)
	}
}

func (p *rxPreconnect) fill(ctx context.Context) {
	failures := int32(0)
	for {
		p.mu.Lock()
		full, closed := len(p.queue) >= int(p.opts.Preconnects), p.closed
		p.mu.Unlock()
		if closed {
			return
		}
		if full {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
			}
			continue
		}
		conn, err := p.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			errors.LogInfo(ctx, "Xray-RX: preconnect failed, count ", failures, ", error ", err)
			if failures >= p.opts.MaxReconnects {
				return
			}
			timer := time.NewTimer(min(2*time.Second, time.Duration(p.opts.RetryDelayMs)*time.Millisecond*time.Duration(1<<min(failures-1, 10))))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		p.mu.Lock()
		if p.closed || ctx.Err() != nil {
			p.mu.Unlock()
			rxCloseConn(conn)
			return
		}
		p.queue = append(p.queue, rxWarmConn{conn, time.Now()})
		p.mu.Unlock()
	}
}

func (p *rxPreconnect) connect(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.opts.RequestTimeoutMs)*time.Millisecond)
	defer cancel()
	conn, err := p.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err == nil {
		protocol := ""
		if c, ok := conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
			state := c.ConnectionState()
			if !state.HandshakeComplete {
				err = fmt.Errorf("Xray-RX incomplete TLS handshake")
			}
			protocol = state.NegotiatedProtocol
		} else if c, ok := conn.(*reality.UConn); ok {
			state := c.ConnectionState()
			if !state.HandshakeComplete {
				err = fmt.Errorf("Xray-RX incomplete REALITY handshake")
			}
			protocol = state.NegotiatedProtocol
		}
		if (p.version == "2" && protocol != "h2") || (p.version == "1.1" && protocol != "" && protocol != "http/1.1") {
			err = fmt.Errorf("Xray-RX unexpected ALPN %q for HTTP/%s", protocol, p.version)
		}
	}
	if err != nil {
		rxCloseConn(conn)
		return nil, err
	}
	return conn, nil
}

func (p *rxPreconnect) take(ctx context.Context) (net.Conn, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, context.Canceled
		}
		if len(p.queue) == 0 {
			p.coldDials++
			p.mu.Unlock()
			return p.connect(ctx)
		}
		warm := p.queue[0]
		copy(p.queue, p.queue[1:])
		p.queue[len(p.queue)-1] = rxWarmConn{}
		p.queue = p.queue[:len(p.queue)-1]
		select {
		case p.wake <- struct{}{}:
		default:
		}
		p.mu.Unlock()
		if ctx.Err() != nil || time.Since(warm.at) > time.Duration(p.opts.RequestTimeoutMs)*time.Millisecond {
			rxCloseConn(warm.conn)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		p.mu.Lock()
		p.warmHits++
		p.mu.Unlock()
		return warm.conn, nil
	}
}

func (p *rxPreconnect) close() {
	p.mu.Lock()
	p.closed = true
	queue, cancel := p.queue, p.cancel
	p.queue = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, warm := range queue {
		rxCloseConn(warm.conn)
	}
}

func (p *rxPreconnect) client() *http.Client {
	return &http.Client{Transport: &rxOneShotTransport{pool: p}}
}

// ClientConn.RoundTrip has no Transport retry loop. One GET owns one TCP even
// after GOAWAY/REFUSED_STREAM; recovery belongs to rxDownlink, not net/http.
type rxOneShotTransport struct {
	pool   *rxPreconnect
	used   atomic.Bool
	mu     sync.Mutex
	conn   net.Conn
	closed bool
}

func (t *rxOneShotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.used.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("Xray-RX transport already used")
	}
	conn, err := t.pool.take(req.Context())
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.closed || req.Context().Err() != nil {
		t.mu.Unlock()
		rxCloseConn(conn)
		return nil, context.Canceled
	}
	t.conn = conn
	t.mu.Unlock()
	// Also covers a blocked H2 preface write, before GotConn can run.
	context.AfterFunc(req.Context(), t.CloseIdleConnections)
	if t.pool.version == "2" {
		tr := &http2.Transport{}
		cc, err := tr.NewClientConn(conn)
		if err != nil {
			t.CloseIdleConnections()
			return nil, err
		}
		if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.GotConn != nil {
			trace.GotConn(httptrace.GotConnInfo{Conn: conn})
		}
		return cc.RoundTrip(req)
	}
	var dialed atomic.Bool
	dial := func(context.Context, string, string) (net.Conn, error) {
		if !dialed.CompareAndSwap(false, true) {
			return nil, fmt.Errorf("Xray-RX transport attempted a second dial")
		}
		return conn, nil
	}
	tr := &http.Transport{DialContext: dial, DialTLSContext: dial, DisableKeepAlives: true}
	return tr.RoundTrip(req)
}

func (t *rxOneShotTransport) CloseIdleConnections() {
	t.mu.Lock()
	t.closed = true
	conn := t.conn
	t.mu.Unlock()
	if conn != nil {
		rxCloseConn(conn)
	}
}

func rxSocketID(conn net.Conn) uint64 {
	for {
		if c, ok := conn.(*rxSocket); ok {
			return c.id
		}
		if c, ok := conn.(interface{ NetConn() net.Conn }); ok {
			conn = c.NetConn()
			continue
		}
		return 0
	}
}

// ponytail: fixed hedge timing from field captures (some full handshakes stalled
// about 4.6s and then went silent, others finished in about 100ms; a healthy
// resumed handshake takes about 100ms, so 500ms rarely hedges normal dials); make
// it a config field only if another path needs different values.
const (
	rxHedgeDelay    = 500 * time.Millisecond
	rxHedgeAttempts = 4
)

// rxHedgeDial starts another TCP+TLS attempt while earlier handshakes are still
// pending, returns the first success and closes every other attempt. The winner's
// context is never cancelled here, so the connection lifetime is unchanged.
func rxHedgeDial(dial func(context.Context) (net.Conn, error)) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		type result struct {
			index     int
			conn      net.Conn
			err       error
			stopClose func() bool
		}
		results := make(chan result, rxHedgeAttempts)
		var cancels []context.CancelFunc
		winner := -1
		defer func() {
			for i, cancel := range cancels {
				if i != winner {
					cancel()
				}
			}
		}()
		launch := func() {
			attemptCtx, cancel := context.WithCancel(ctx)
			cancels = append(cancels, cancel)
			index := len(cancels) - 1
			if index > 0 {
				errors.LogInfo(ctx, "Xray-RX: hedged dial attempt ", index+1)
			}
			go func() {
				conn, err := dial(attemptCtx)
				// A cancelled caller must not wait for an embedding dialer.
				// Every loser, including a late success, closes itself on cancel.
				stopClose := context.AfterFunc(attemptCtx, func() {
					if conn != nil {
						rxCloseConn(conn)
					}
				})
				if conn == nil && err == nil {
					err = fmt.Errorf("Xray-RX dial returned a nil connection")
				}
				results <- result{index, conn, err, stopClose}
			}()
		}
		launch()
		pending := 1
		timer := time.NewTimer(rxHedgeDelay)
		defer timer.Stop()
		var lastErr error
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				if len(cancels) < rxHedgeAttempts && ctx.Err() == nil {
					launch()
					pending++
					timer.Reset(rxHedgeDelay)
				}
			case r := <-results:
				pending--
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if r.err == nil {
					if !r.stopClose() {
						return nil, ctx.Err()
					}
					winner = r.index
					return r.conn, nil
				}
				cancels[r.index]()
				lastErr = r.err
				if len(cancels) < rxHedgeAttempts && ctx.Err() == nil {
					launch()
					pending++
					timer.Reset(rxHedgeDelay)
				} else if pending == 0 {
					return nil, lastErr
				}
			}
		}
	}
}

// rxSpread chooses the TCP destination of each new RX connection. Field data
// showed anycast IPs of one host ranging from dead to frozen-after-handshake to
// healthy, so every address keeps a success EWMA fed by dials and segment results.
// ponytail: power-of-two-choices on a global mutex; per-address locks or a
// heap only if thousands of addresses or dials per second need it.
type rxSpread struct {
	mu        sync.Mutex
	addrs     []string
	scores    []float64
	failures  []uint32
	coolUntil []time.Time
}

func newRXSpread(addrs []string) *rxSpread {
	s := &rxSpread{addrs: append([]string(nil), addrs...), scores: make([]float64, len(addrs)),
		failures: make([]uint32, len(addrs)), coolUntil: make([]time.Time, len(addrs))}
	for i := range s.scores {
		s.scores[i] = 1
	}
	return s
}

// pick samples two eligible addresses. Repeated failures cool down briefly;
// expiry permits recovery probes, and an entirely cold pool still makes progress.
func (s *rxSpread) pick() int {
	a, b := rand.IntN(len(s.addrs)), rand.IntN(len(s.addrs))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	aCold, bCold := now.Before(s.coolUntil[a]), now.Before(s.coolUntil[b])
	if aCold && bCold {
		best := -1
		for i := range s.addrs {
			if !now.Before(s.coolUntil[i]) && (best < 0 || s.scores[i] > s.scores[best]) {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
		// Do not permanently exclude the pool when all paths have failed.
		for i := range s.addrs {
			if s.coolUntil[i].Before(s.coolUntil[a]) {
				a = i
			}
		}
		return a
	}
	if aCold {
		return b
	}
	if bCold {
		return a
	}
	if s.scores[b] > s.scores[a] {
		return b
	}
	return a
}

func (s *rxSpread) report(i int, ok bool) {
	v := 0.0
	if ok {
		v = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scores[i] = 0.9*s.scores[i] + 0.1*v
	if ok {
		s.failures[i] = 0
		s.coolUntil[i] = time.Time{}
	} else {
		s.failures[i] = min(s.failures[i]+1, uint32(32))
		if s.failures[i] >= 2 {
			delay := min(30*time.Second, time.Second*time.Duration(1<<min(s.failures[i]-2, uint32(5))))
			s.coolUntil[i] = time.Now().Add(delay)
		}
	}
}

// rxReportConn records whether a connection delivered its segment; only the
// first report per TCP counts.
func rxReportConn(conn net.Conn, ok bool) {
	for {
		if c, ok2 := conn.(*rxSocket); ok2 {
			if c.spread != nil && c.reported.CompareAndSwap(false, true) {
				c.spread.report(c.spreadIndex, ok)
			}
			return
		}
		if c, ok2 := conn.(interface{ NetConn() net.Conn }); ok2 {
			conn = c.NetConn()
			continue
		}
		return
	}
}

func rxCloseConn(conn net.Conn) {
	if c, ok := conn.(interface{ SuppressCloseNotify() }); ok {
		c.SuppressCloseNotify()
	}
	// 受限连接在第 10 个下行包后不再收到 FIN-ACK，直接置 Linger(0) 发送 RST 避免内核重传风暴。
	for u := conn; u != nil; {
		if tcp, ok := u.(interface{ SetLinger(int) error }); ok {
			_ = tcp.SetLinger(0)
			break
		}
		if w, ok := u.(interface{ NetConn() net.Conn }); ok {
			u = w.NetConn()
			continue
		}
		break
	}
	conn.Close()
}

var rxConnectionID atomic.Uint64

// This wrapper sits below TLS. With FinalMask it measures the DialTCP result,
// not physical-link bytes; neither level includes IP headers or retransmits.
type rxSocket struct {
	net.Conn
	ctx     context.Context
	id      uint64
	started time.Time
	read    atomic.Uint64
	written atomic.Uint64
	once    sync.Once

	spread      *rxSpread
	spreadIndex int
	reported    atomic.Bool
}

func (c *rxSocket) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(uint64(n))
	return n, err
}

func (c *rxSocket) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(uint64(n))
	return n, err
}

func (c *rxSocket) Close() error {
	if c.spread != nil {
		if tcp, ok := c.Conn.(interface{ SetLinger(int) error }); ok {
			_ = tcp.SetLinger(0)
		}
	}
	err := c.Conn.Close()
	c.once.Do(func() {
		errors.LogInfo(c.ctx, "Xray-RX: TCP closed, id ", c.id, ", read ", c.read.Load(), ", written ", c.written.Load(), ", lifetime_ms ", time.Since(c.started).Milliseconds(), ", reason owner-close")
	})
	return err
}
