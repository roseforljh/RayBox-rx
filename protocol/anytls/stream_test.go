package anytls

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/anytls/sing-anytls/padding"
	"github.com/anytls/sing-anytls/session"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/sagernet/sing/common/atomic"
)

func TestFramedStreamLargeWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	local.SetDeadline(time.Now().Add(3 * time.Second))
	remote.SetDeadline(time.Now().Add(3 * time.Second))
	var scheme atomic.TypedValue[*padding.PaddingFactory]
	if !padding.UpdatePaddingScheme(padding.DefaultPaddingScheme, &scheme) {
		t.Fatal("invalid default padding")
	}
	client := session.NewClientSession(local, &scheme, log.NewNOPFactory().Logger())
	client.Run()
	defer client.Close()
	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("raybox-field-verified-"), 4096)
	other := bytes.Repeat([]byte("second-frame-verified-"), 4096)
	wrapped := &framedStream{Stream: stream}
	written := make(chan error, 2)
	for _, data := range [][]byte{payload, other} {
		go func() {
			n, err := wrapped.Write(data)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
			written <- err
		}()
	}
	var received []byte
	var lengths []int
	for len(received) < len(payload)+len(other) {
		var header [7]byte
		if _, err := io.ReadFull(remote, header[:]); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, binary.BigEndian.Uint16(header[5:]))
		if _, err := io.ReadFull(remote, data); err != nil {
			t.Fatal(err)
		}
		if header[0] == 2 {
			lengths = append(lengths, len(data))
			received = append(received, data...)
		}
	}
	for range 2 {
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	}
	if len(lengths) != 4 || lengths[0] != 65535 || lengths[1] != 24577 ||
		lengths[2] != 65535 || lengths[3] != 24577 {
		t.Fatalf("unexpected data frames: %v", lengths)
	}
	first := append(append([]byte(nil), payload...), other...)
	second := append(append([]byte(nil), other...), payload...)
	if !bytes.Equal(received, first) && !bytes.Equal(received, second) {
		t.Fatal("concurrent writes interleaved or corrupted payload")
	}
}
