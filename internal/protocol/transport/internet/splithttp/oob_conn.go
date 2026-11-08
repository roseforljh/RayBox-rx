package splithttp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
)

// oobConn only changes the first TLS write. The final byte passed to sendUrgent
// is urgent; any preceding byte remains part of the ordinary TCP byte stream.
type oobConn struct {
	net.Conn
	ctx      context.Context
	position int
	delay    time.Duration
	mu       sync.Mutex
	sent     bool
	err      error
}

func (c *oobConn) NetConn() net.Conn { return c.Conn }

func oobRawConn(conn net.Conn) (syscall.RawConn, error) {
	for range 32 {
		if s, ok := conn.(syscall.Conn); ok {
			return s.SyscallConn()
		}
		switch c := conn.(type) {
		case interface{ NetConn() net.Conn }:
			conn = c.NetConn()
		case interface{ Upstream() any }:
			var ok bool
			conn, ok = c.Upstream().(net.Conn)
			if !ok {
				return nil, fmt.Errorf("OOB requires a native TCP socket")
			}
		default:
			return nil, fmt.Errorf("OOB requires a native TCP socket")
		}
	}
	return nil, fmt.Errorf("OOB socket wrapper chain is too deep")
}

func (c *oobConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	if len(p) == 0 || c.sent {
		return c.Conn.Write(p)
	}
	c.sent = true
	if len(p) < 5 || p[0] != 22 {
		c.err = fmt.Errorf("OOB first write must be a TLS ClientHello")
		return 0, c.err
	}
	if c.err = c.ctx.Err(); c.err != nil {
		return 0, c.err
	}
	raw, err := oobRawConn(c.Conn)
	if err != nil {
		c.err = err
		return 0, err
	}
	// A single syscall matches the tested ByeDPI primitive, including position 1.
	packet := append(append([]byte(nil), p[:c.position]...), 'X')
	n, err := sendUrgent(raw, packet)
	if err == nil && n != len(packet) {
		err = io.ErrShortWrite
	}
	if err != nil {
		c.err = err
		return max(0, min(n, c.position)), err
	}
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		c.err = c.ctx.Err()
		return c.position, c.err
	case <-timer.C:
	}
	n, err = c.Conn.Write(p[c.position:])
	c.err = err
	return c.position + n, err
}
