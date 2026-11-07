package anytls

import (
	"io"
	"sync"

	"github.com/anytls/sing-anytls/session"
)

// AnyTLS encodes each data frame length as uint16. Keep large application
// writes within that limit while retaining stream handshake and deadline methods.
type framedStream struct {
	*session.Stream
	writeAccess sync.Mutex
}

func (s *framedStream) Write(p []byte) (int, error) {
	s.writeAccess.Lock()
	defer s.writeAccess.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), 1<<16-1)]
		n, err := s.Stream.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		if n != len(chunk) {
			return written, io.ErrShortWrite
		}
		p = p[n:]
	}
	return written, nil
}
