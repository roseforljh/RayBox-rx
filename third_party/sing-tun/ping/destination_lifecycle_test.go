package ping

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDestinationCloseCallbacksRunExactlyOnce(t *testing.T) {
	accepted, peer := net.Pipe()
	defer peer.Close()
	destination := &Destination{
		conn:      &Conn{conn: accepted},
		closeDone: make(chan struct{}),
	}
	var beforeClose atomic.Int32
	destination.AddCloseCallback(func() { beforeClose.Add(1) })

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = destination.Close()
		}()
	}
	wait.Wait()
	if beforeClose.Load() != 1 {
		t.Fatalf("callback registered before close ran %d times", beforeClose.Load())
	}

	var afterClose atomic.Int32
	destination.AddCloseCallback(func() { afterClose.Add(1) })
	if afterClose.Load() != 1 {
		t.Fatalf("callback registered after close ran %d times", afterClose.Load())
	}
}
