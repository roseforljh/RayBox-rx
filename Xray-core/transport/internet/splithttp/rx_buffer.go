package splithttp

import (
	"context"
	"errors"
	"io"
	"sync"
)

var errRXOffset = errors.New("Xray-RX offset outside retained window")

// rxBuffer retains every unacknowledged byte. A full buffer applies backpressure
// even when a downstream HTTP writer is attached. No network I/O holds mu.
type rxBuffer struct {
	writeMu           sync.Mutex
	mu                sync.Mutex
	data              []byte
	base, end         uint64
	finished, aborted bool
	changed           chan struct{}
}

func newRXBuffer(capacity int) *rxBuffer {
	return &rxBuffer{data: make([]byte, capacity), changed: make(chan struct{})}
}

func (b *rxBuffer) signal() { close(b.changed); b.changed = make(chan struct{}) }

func (b *rxBuffer) Write(p []byte) (int, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	n := 0
	for len(p) > 0 {
		b.mu.Lock()
		if b.finished || b.aborted {
			b.mu.Unlock()
			return n, io.ErrClosedPipe
		}
		space := uint64(len(b.data)) - (b.end - b.base)
		if space == 0 {
			changed := b.changed
			b.mu.Unlock()
			<-changed
			continue
		}
		pos := int(b.end % uint64(len(b.data)))
		count := min(len(p), int(space), len(b.data)-pos)
		copy(b.data[pos:pos+count], p[:count])
		b.end += uint64(count)
		b.signal()
		b.mu.Unlock()
		n += count
		p = p[count:]
	}
	return n, nil
}

func (b *rxBuffer) state(offset uint64, acknowledge bool) (uint64, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aborted {
		return b.end, b.finished, io.ErrClosedPipe
	}
	if offset > b.end || (!acknowledge && offset < b.base) {
		return b.end, b.finished, errRXOffset
	}
	if acknowledge && offset > b.base {
		b.base = offset
		b.signal()
	}
	return b.end, b.finished, nil
}

func (b *rxBuffer) read(ctx context.Context, offset uint64) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.aborted {
			b.mu.Unlock()
			return nil, io.ErrClosedPipe
		}
		if offset < b.base || offset > b.end {
			b.mu.Unlock()
			return nil, errRXOffset
		}
		if offset < b.end {
			pos := int(offset % uint64(len(b.data)))
			// Flush small HTTP writes so a frozen 15–20 KiB connection can
			// deliver complete H2 frames/TLS records before it stops. This is
			// not PacketDown framing or a universal filtering threshold: TLS
			// overhead can still consume the whole budget and force failure.
			n := min(int(b.end-offset), len(b.data)-pos, 4*1024)
			p := append([]byte(nil), b.data[pos:pos+n]...)
			b.mu.Unlock()
			return p, nil
		}
		if b.finished {
			b.mu.Unlock()
			return nil, io.EOF
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// readReady copies an already produced segment, including a ring wrap, under
// one lock. The caller bounds end by the negotiated response size.
func (b *rxBuffer) readReady(offset, end uint64) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aborted {
		return nil, io.ErrClosedPipe
	}
	if offset < b.base || offset > end || end > b.end {
		return nil, errRXOffset
	}
	p := make([]byte, int(end-offset))
	pos := int(offset % uint64(len(b.data)))
	n := copy(p, b.data[pos:])
	copy(p[n:], b.data[:len(p)-n])
	return p, nil
}

// Close preserves the final bytes until their ACK or session expiry.
func (b *rxBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.finished {
		b.finished = true
		b.signal()
	}
	return nil
}

func (b *rxBuffer) abort() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.aborted {
		b.aborted = true
		b.data = nil
		b.signal()
	}
}
