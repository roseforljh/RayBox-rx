package mux

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/pipe"
)

type checkedMuxWriter struct {
	active      atomic.Int32
	overlap     atomic.Bool
	calls       atomic.Int32
	closed      atomic.Bool
	interrupted atomic.Bool
}

func (w *checkedMuxWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	defer buf.ReleaseMulti(mb)
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	runtime.Gosched()
	w.calls.Add(1)
	w.active.Add(-1)
	return nil
}
func (w *checkedMuxWriter) Close() error { w.closed.Store(true); return nil }
func (w *checkedMuxWriter) Interrupt()   { w.interrupted.Store(true) }

func TestMuxWorkerSerializesWrites(t *testing.T) {
	for _, side := range []string{"client", "server"} {
		t.Run(side, func(t *testing.T) {
			under := &checkedMuxWriter{}
			r, pw := pipe.New()
			defer pw.Close()
			link := &transport.Link{Reader: r, Writer: under}
			var output buf.Writer
			if side == "client" {
				worker, err := NewClientWorker(*link, ClientStrategy{})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Close()
				output = worker.link.Writer
			} else {
				worker, err := NewServerWorker(context.Background(), nil, link)
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Close()
				output = worker.link.Writer
				if link.Writer != under {
					t.Fatal("caller link mutated")
				}
			}
			if _, ok := output.(*MutexWriter); !ok {
				t.Fatal("worker missing shared writer lock")
			}
			var wg sync.WaitGroup
			for id := range 16 {
				wg.Go(func() {
					writer := NewResponseWriter(uint16(id), output, protocol.TransferTypeStream)
					for range 32 {
						b := buf.New()
						b.WriteString("payload")
						if err := writer.WriteMultiBuffer(buf.MultiBuffer{b}); err != nil {
							t.Error(err)
							return
						}
					}
					writer.Close()
				})
			}
			wg.Wait()
			if under.overlap.Load() || under.calls.Load() != 16*33 {
				t.Fatalf("overlap=%v, calls=%d", under.overlap.Load(), under.calls.Load())
			}
			if err := common.Close(output); err != nil || !under.closed.Load() {
				t.Fatal("Close not forwarded")
			}
			common.Interrupt(output)
			if !under.interrupted.Load() {
				t.Fatal("Interrupt not forwarded")
			}
		})
	}
}
