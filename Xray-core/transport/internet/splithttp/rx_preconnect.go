package splithttp

import (
	"context"
	"crypto/tls"
	"fmt"
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
			index int
			conn  net.Conn
			err   error
		}
		results := make(chan result, rxHedgeAttempts)
		var cancels []context.CancelFunc
		launch := func() {
			attemptCtx, cancel := context.WithCancel(ctx)
			cancels = append(cancels, cancel)
			index := len(cancels) - 1
			if index > 0 {
				errors.LogInfo(ctx, "Xray-RX: hedged dial attempt ", index+1)
			}
			go func() {
				conn, err := dial(attemptCtx)
				results <- result{index, conn, err}
			}()
		}
		launch()
		pending := 1
		timer := time.NewTimer(rxHedgeDelay)
		defer timer.Stop()
		var lastErr error
		for {
			select {
			case <-timer.C:
				if len(cancels) < rxHedgeAttempts && ctx.Err() == nil {
					launch()
					pending++
					timer.Reset(rxHedgeDelay)
				}
			case r := <-results:
				pending--
				if r.err == nil {
					for i, cancel := range cancels {
						if i != r.index {
							cancel()
						}
					}
					go func(n int) {
						for ; n > 0; n-- {
							if late := <-results; late.err == nil {
								rxCloseConn(late.conn)
							}
						}
					}(pending)
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

func rxCloseConn(conn net.Conn) {
	if c, ok := conn.(interface{ SuppressCloseNotify() }); ok {
		c.SuppressCloseNotify()
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
	err := c.Conn.Close()
	c.once.Do(func() {
		errors.LogInfo(c.ctx, "Xray-RX: TCP closed, id ", c.id, ", read ", c.read.Load(), ", written ", c.written.Load(), ", lifetime_ms ", time.Since(c.started).Milliseconds(), ", reason owner-close")
	})
	return err
}
