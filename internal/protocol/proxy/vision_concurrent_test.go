package proxy_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy"
)

// Reproduces XTLS/Xray-core#7337: the first padded block must reach the wire
// before another mux session can change padding state and write its block.
type holdFirstVisionWriter struct {
	sync.Mutex
	out     bytes.Buffer
	calls   int
	entered chan struct{}
	landed  chan struct{}
}

func (w *holdFirstVisionWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.Lock()
	w.calls++
	call := w.calls
	w.Unlock()
	if call == 1 {
		close(w.entered)
		select {
		case <-w.landed:
		case <-time.After(200 * time.Millisecond):
		}
	}
	w.Lock()
	for _, b := range mb {
		w.out.Write(b.Bytes())
	}
	w.Unlock()
	buf.ReleaseMulti(mb)
	if call == 2 {
		close(w.landed)
	}
	return nil
}

func TestVisionWriterConcurrentWrites(t *testing.T) {
	for _, uplink := range []bool{false, true} {
		t.Run(map[bool]string{false: "downlink", true: "uplink"}[uplink], func(t *testing.T) {
			uuid := []byte("0123456789abcdef")
			under := &holdFirstVisionWriter{entered: make(chan struct{}), landed: make(chan struct{})}
			writer := proxy.NewVisionWriter(under, proxy.NewTrafficState(uuid), uplink, context.Background(), nil, nil, nil)
			payloads := [][]byte{bytes.Repeat([]byte{'a'}, 40), bytes.Repeat([]byte{'b'}, 40)}
			write := func(p []byte) error {
				b := buf.New()
				b.Write(p)
				return writer.WriteMultiBuffer(buf.MultiBuffer{b})
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Add(1)
			go func() { defer wg.Done(); errs <- write(payloads[0]) }()
			<-under.entered
			wg.Add(1)
			go func() { defer wg.Done(); errs <- write(payloads[1]) }()
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			reader := proxy.NewVisionReader(buf.NewReader(bytes.NewReader(under.out.Bytes())), proxy.NewTrafficState(uuid), uplink, context.Background(), nil, nil, nil, nil)
			var got bytes.Buffer
			if err := buf.Copy(reader, buf.NewWriter(&got)); err != nil {
				t.Fatal(err)
			}
			want := append(append([]byte{}, payloads[0]...), payloads[1]...)
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("payloads changed after concurrent padding: got %d bytes, want %d", got.Len(), len(want))
			}
		})
	}
}
