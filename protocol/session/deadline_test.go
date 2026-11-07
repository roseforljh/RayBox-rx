package session

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	xbuf "github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net/cnc"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/pipe"
	"github.com/sagernet/sing/common/bufio/deadline"
)

func TestTCPCombinedDeadline(t *testing.T) {
	reader, writer := pipe.New()
	defer writer.Close()
	conn := cnc.NewConnection(cnc.ConnectionInput(io.Discard), cnc.ConnectionOutputMulti(reader))
	wrapped := &deadlineConn{ExtendedConn: deadline.NewConn(conn)}
	defer wrapped.Close()
	if err := wrapped.SetDeadline(time.Now().Add(20 * time.Millisecond)); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("write deadline must report unsupported, got %v", err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := wrapped.Read(make([]byte, 1))
		finished <- err
	}()
	select {
	case err := <-finished:
		if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			t.Fatalf("expected timeout, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SetDeadline bypassed the read deadline wrapper")
	}
	if err := wrapped.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteMultiBuffer(xbuf.MergeBytes(nil, []byte("a"))); err != nil {
		t.Fatal(err)
	}
	var received [1]byte
	if _, err := io.ReadFull(wrapped, received[:]); err != nil || received[0] != 'a' {
		t.Fatalf("updated deadline failed: %q, %v", received, err)
	}
	if err := wrapped.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteMultiBuffer(xbuf.MergeBytes(nil, []byte("b"))); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(wrapped, received[:]); err != nil || received[0] != 'b' {
		t.Fatalf("cleared deadline failed: %q, %v", received, err)
	}
	if err := wrapped.SetWriteDeadline(time.Now()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("unsupported write deadline reported success: %v", err)
	}
}
