package splithttp

import (
	"bufio"
	"encoding/binary"
	"io"
	"sync"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/errors"
)

// Packet-down is negotiated per HTTP response. It keeps an idle downlink alive;
// it does not recover a response truncated by a byte limit.
const (
	pdRequestHeader            = "X-Packet-Down"
	pdResponseHeader           = "X-Packet-Down"
	pdTypeData            byte = 1
	pdTypePadding         byte = 2
	pdFlagSessionID       byte = 1
	pdFlagSeq             byte = 2
	pdSessionIDExcerptLen      = 8
	pdMaxFramePayload          = 0xffff
)

type pdFrame struct {
	sessionIDExcerpt []byte
	seq              uint64
	hasSeq           bool
	typ              byte
	payload          []byte
}

func pdWriteFrame(w io.Writer, f pdFrame) error {
	if len(f.payload) > pdMaxFramePayload || (len(f.sessionIDExcerpt) != 0 && len(f.sessionIDExcerpt) != pdSessionIDExcerptLen) {
		return errors.New("invalid packet-down frame length")
	}
	if f.typ != pdTypeData && f.typ != pdTypePadding {
		return errors.New("invalid packet-down frame type")
	}
	header := make([]byte, 4, 20+len(f.payload))
	header[1] = f.typ
	binary.BigEndian.PutUint16(header[2:], uint16(len(f.payload)))
	if len(f.sessionIDExcerpt) != 0 {
		header[0] |= pdFlagSessionID
		header = append(header, f.sessionIDExcerpt...)
	}
	if f.hasSeq {
		header[0] |= pdFlagSeq
		header = binary.BigEndian.AppendUint64(header, f.seq)
	}
	frame := append(header, f.payload...)
	n, err := w.Write(frame)
	if err == nil && n != len(frame) {
		return io.ErrShortWrite
	}
	return err
}

func pdReadFrame(r *bufio.Reader) (pdFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return pdFrame{}, err
	}
	if header[0] & ^byte(pdFlagSessionID|pdFlagSeq) != 0 || (header[1] != pdTypeData && header[1] != pdTypePadding) {
		return pdFrame{}, errors.New("invalid packet-down frame flags or type")
	}
	f := pdFrame{typ: header[1]}
	if header[0]&pdFlagSessionID != 0 {
		f.sessionIDExcerpt = make([]byte, pdSessionIDExcerptLen)
		if _, err := io.ReadFull(r, f.sessionIDExcerpt); err != nil {
			return pdFrame{}, err
		}
	}
	if header[0]&pdFlagSeq != 0 {
		var seq [8]byte
		if _, err := io.ReadFull(r, seq[:]); err != nil {
			return pdFrame{}, err
		}
		f.seq, f.hasSeq = binary.BigEndian.Uint64(seq[:]), true
	}
	f.payload = make([]byte, binary.BigEndian.Uint16(header[2:]))
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return pdFrame{}, err
	}
	return f, nil
}

func pdSessionIDExcerpt(id string) []byte {
	if len(id) < pdSessionIDExcerptLen {
		return nil
	}
	return []byte(id[:pdSessionIDExcerptLen])
}

type packetDownWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	sessionID string
	config    *PacketDownConfig
	seq       uint64
	lastWrite time.Time
	err       error
	closed    bool
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
	stop      chan struct{}
	done      chan struct{}
}

func newPacketDownWriter(w io.Writer, sessionID string, config *PacketDownConfig) *packetDownWriter {
	d := &packetDownWriter{writer: w, sessionID: sessionID, config: config, lastWrite: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	if config.GetKeepAlive() {
		go d.keepAliveLoop()
	} else {
		close(d.done)
	}
	return d
}

func (d *packetDownWriter) frame(typ byte, payload []byte) error {
	excerpt := pdSessionIDExcerpt(d.sessionID)
	err := pdWriteFrame(d.writer, pdFrame{sessionIDExcerpt: excerpt, seq: d.seq, hasSeq: len(excerpt) > 0, typ: typ, payload: payload})
	if err != nil {
		d.err = err
		return err
	}
	d.seq++
	d.lastWrite = time.Now()
	return nil
}

func (d *packetDownWriter) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return 0, io.ErrClosedPipe
	}
	if d.err != nil {
		return 0, d.err
	}
	written := 0
	for len(b) > 0 {
		n := min(len(b), pdMaxFramePayload)
		if err := d.frame(pdTypeData, b[:n]); err != nil {
			return written, err
		}
		written += n
		b = b[n:]
	}
	return written, nil
}

func (d *packetDownWriter) keepAliveLoop() {
	defer close(d.done)
	for {
		period := time.Duration(d.config.GetNormalizedKeepAlivePeriod().rand()) * time.Second
		timer := time.NewTimer(period)
		select {
		case <-d.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		d.mu.Lock()
		select {
		case <-d.stop:
			d.mu.Unlock()
			return
		default:
		}
		if d.closed || d.err != nil {
			d.mu.Unlock()
			return
		}
		if time.Since(d.lastWrite) >= period {
			padding, ok := randStringFromCharset(int(d.config.GetNormalizedKeepAliveLength().rand()), charsetBase62)
			var err error
			if !ok {
				err = errors.New("failed to generate packet-down padding")
			} else {
				err = d.frame(pdTypePadding, []byte(padding))
			}
			if err != nil {
				d.err = err
				d.mu.Unlock()
				return
			}
		}
		d.mu.Unlock()
	}
}

// Stop cancels the timer and waits until no padding write remains. HTTP/socket
// writers get an immediate deadline so shutdown cannot wait on a stalled peer.
func (d *packetDownWriter) Stop() {
	d.stopOnce.Do(func() { close(d.stop) })
	if !d.mu.TryLock() {
		if deadlineWriter, ok := d.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadlineWriter.SetWriteDeadline(time.Now())
		}
		d.mu.Lock()
	}
	d.mu.Unlock()
	<-d.done
	if deadlineWriter, ok := d.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = deadlineWriter.SetWriteDeadline(time.Time{})
	}
}

func (d *packetDownWriter) Close() error {
	d.closeOnce.Do(func() {
		d.stopOnce.Do(func() { close(d.stop) })
		if !d.mu.TryLock() {
			if deadlineWriter, ok := d.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
				_ = deadlineWriter.SetWriteDeadline(time.Now())
			}
			d.mu.Lock()
		}
		d.closed = true
		d.mu.Unlock()
		<-d.done
		if deadlineWriter, ok := d.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadlineWriter.SetWriteDeadline(time.Time{})
		}
		if closer, ok := d.writer.(io.Closer); ok {
			d.closeErr = closer.Close()
		}
	})
	return d.closeErr
}

type packetDownReader struct {
	reader   io.ReadCloser
	buffered *bufio.Reader
	pending  []byte
}

func newPacketDownReader(reader io.ReadCloser) *packetDownReader {
	return &packetDownReader{reader: reader, buffered: bufio.NewReaderSize(reader, 16384)}
}

func (d *packetDownReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for len(d.pending) == 0 {
		frame, err := pdReadFrame(d.buffered)
		if err != nil {
			return 0, err
		}
		if frame.typ == pdTypeData {
			d.pending = frame.payload
		}
	}
	n := copy(b, d.pending)
	d.pending = d.pending[n:]
	return n, nil
}

func (d *packetDownReader) Close() error { return d.reader.Close() }
