package splithttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/errors"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/browser_dialer"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
	"google.golang.org/protobuf/proto"
)

type oobCandidate struct {
	dest      net.Destination
	stream    *internet.MemoryStreamConfig
	failures  int
	coolUntil time.Time
}

// Stored on the existing cached HTTP client, scoped by destination/config.
// No payloads, credentials, or unbounded connection history are retained.
type oobPolicy struct {
	mu             sync.Mutex
	candidates     []oobCandidate
	preferred      int
	preferredUntil time.Time
	next           int
	fallback       *internet.MemoryStreamConfig
}

func newOOBPolicy(dest net.Destination, stream *internet.MemoryStreamConfig, o *OOBConfig) (*oobPolicy, error) {
	p := &oobPolicy{preferred: -1}
	addresses := o.Addresses
	if len(addresses) == 0 {
		addresses = []string{dest.Address.String()}
	}
	// Spread initial attempts across entries, rather than always choosing IP 0.
	for variantIndex := 0; variantIndex < len(o.Positions)*len(o.HttpVersions); variantIndex++ {
		for addressIndex, address := range addresses {
			pos := o.Positions[(variantIndex+addressIndex)%len(o.Positions)]
			version := o.HttpVersions[(variantIndex/len(o.Positions)+addressIndex)%len(o.HttpVersions)]
			c := proto.Clone(stream.ProtocolSettings.(*Config)).(*Config)
			c.Oob = proto.Clone(o).(*OOBConfig)
			c.Oob.Positions, c.Oob.Addresses, c.Oob.FallbackSettings = []int32{pos}, nil, nil
			variant := *stream
			variant.ProtocolSettings = c
			security := proto.Clone(tls.ConfigFromStreamSettings(stream)).(*tls.Config)
			security.NextProtocol = []string{version}
			variant.SecuritySettings = security
			variant.SocketSettings = &internet.SocketConfig{}
			if stream.SocketSettings != nil {
				variant.SocketSettings = proto.Clone(stream.SocketSettings).(*internet.SocketConfig)
			}
			variant.SocketSettings.TcpMaxSeg = max(0, o.TcpMaxSeg)
			target := dest
			target.Address = net.ParseAddress(address)
			p.candidates = append(p.candidates, oobCandidate{dest: target, stream: &variant})
		}
	}
	if o.FallbackSettings != nil {
		var err error
		p.fallback, err = internet.ToMemoryStreamConfig(o.FallbackSettings)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *oobPolicy) pick(tried map[int]bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	eligible := func(i int) bool { return !tried[i] && !now.Before(p.candidates[i].coolUntil) }
	if p.preferred >= 0 && now.Before(p.preferredUntil) && eligible(p.preferred) {
		return p.preferred
	}
	for n := range p.candidates {
		i := (p.next + n) % len(p.candidates)
		if eligible(i) {
			p.next = (i + 1) % len(p.candidates)
			return i
		}
	}
	return -1
}

func (p *oobPolicy) report(i int, success bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := &p.candidates[i]
	if success {
		c.failures, c.coolUntil = 0, time.Time{}
		p.preferred, p.preferredUntil = i, time.Now().Add(2*time.Minute)
		return
	}
	c.failures = min(c.failures+1, 6)
	c.coolUntil = time.Now().Add(min(30*time.Second, time.Second<<uint(c.failures-1)))
	if p.preferred == i {
		p.preferred = -1
	}
}

func dialOOB(ctx context.Context, dest net.Destination, stream *internet.MemoryStreamConfig, o *OOBConfig) (stat.Connection, error) {
	t := tls.ConfigFromStreamSettings(stream)
	if t == nil || reality.ConfigFromStreamSettings(stream) != nil || decideHTTPVersion(t, nil) == "3" || stream.FinalMask != nil || browser_dialer.HasBrowserDialer() {
		return nil, fmt.Errorf("OOB requires native TCP XHTTP with TLS, without REALITY, H3, FinalMask or Browser Dialer")
	}
	if len(o.Addresses) != 0 && t.ServerName == "" {
		return nil, fmt.Errorf("OOB addresses requires an explicit TLS serverName")
	}
	base, _ := getHTTPClient(ctx, dest, stream)
	client := base.(*DefaultDialerClient)
	var initErr error
	client.oobOnce.Do(func() { client.oob, initErr = newOOBPolicy(dest, stream, o) })
	if initErr != nil {
		return nil, initErr
	}
	p := client.oob
	if p == nil {
		return nil, fmt.Errorf("OOB policy initialization failed")
	}
	tried := make(map[int]bool)
	var lastErr error
	for attempt := 0; attempt < int(o.MaxAttempts); attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		i := p.pick(tried)
		if i < 0 {
			break
		}
		tried[i] = true
		candidateDest, candidateStream := p.candidates[i].dest, p.candidates[i].stream
		conn, err := dialXHTTP(ctx, candidateDest, candidateStream)
		if err == nil {
			errors.LogInfo(ctx, "XHTTP OOB startup ready, candidate ", i, ", position ", candidateStream.ProtocolSettings.(*Config).Oob.Positions[0], ", protocol ", tls.ConfigFromStreamSettings(candidateStream).NextProtocol[0])
			return &oobObservedConn{Connection: conn, ctx: ctx, policy: p, index: i}, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p.report(i, false)
		lastErr = err
		errors.LogInfo(ctx, "XHTTP OOB startup failed; trying another bounded candidate")
	}
	if p.fallback != nil {
		target := dest
		if p.fallback.Destination != nil {
			target = *p.fallback.Destination
		}
		errors.LogInfo(ctx, "XHTTP OOB startup unavailable; using configured RX fallback")
		return Dial(ctx, target, p.fallback)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all OOB candidates are cooling down")
	}
	return nil, fmt.Errorf("OOB startup exhausted its bounded candidates: %w", lastErr)
}

// Count real delivered bytes, not just TLS success or an HTTP 200, before
// remembering a strategy. A post-start read failure penalizes future sessions;
// it never replays the current application's bytes onto another transport.
type oobObservedConn struct {
	stat.Connection
	ctx              context.Context
	policy           *oobPolicy
	index            int
	received         atomic.Int64
	verified, failed atomic.Bool
	closed           atomic.Bool
}

func (c *oobObservedConn) Read(b []byte) (int, error) {
	n, err := c.Connection.Read(b)
	if c.received.Add(int64(n)) >= 32*1024 && c.verified.CompareAndSwap(false, true) {
		c.policy.report(c.index, true)
	}
	if err != nil && err != io.EOF && !c.closed.Load() && c.ctx.Err() == nil && c.failed.CompareAndSwap(false, true) {
		c.policy.report(c.index, false)
	}
	return n, err
}

func (c *oobObservedConn) Close() error { c.closed.Store(true); return c.Connection.Close() }

type oobResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *oobResponseBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

type oobDialBudgetKey struct{}

// Unlike legacy OpenStream, OOB startup waits for verified HTTP response
// headers, so TLS/H2/HTTP failures can fall back before any application upload.
func (c *DefaultDialerClient) openOOBStream(ctx context.Context, url, session string, body io.Reader, uploadOnly bool) (io.ReadCloser, net.Addr, net.Addr, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	requestCtx = context.WithValue(requestCtx, oobDialBudgetKey{}, &atomic.Int32{})
	var remote, local net.Addr
	requestCtx = httptrace.WithClientTrace(requestCtx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { remote, local = info.Conn.RemoteAddr(), info.Conn.LocalAddr() }})
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	c.transportConfig.FillStreamRequest(req, session, "")
	timer := time.AfterFunc(time.Duration(c.transportConfig.Oob.AttemptTimeoutMs)*time.Millisecond, cancel)
	response, err := c.client.Do(req)
	stopped := timer.Stop()
	if err != nil || !stopped || requestCtx.Err() != nil {
		cancel()
		if response != nil {
			response.Body.Close()
		}
		c.client.CloseIdleConnections()
		if err == nil {
			err = context.DeadlineExceeded
		}
		return nil, nil, nil, err
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		response.Body.Close()
		c.client.CloseIdleConnections()
		return nil, nil, nil, fmt.Errorf("OOB XHTTP startup status %d", response.StatusCode)
	}
	return &oobResponseBody{ReadCloser: response.Body, cancel: cancel}, remote, local, nil
}
