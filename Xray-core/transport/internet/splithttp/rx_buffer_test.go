package splithttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRXBufferBackpressureReplayAndWrap(t *testing.T) {
	b := newRXBuffer(8)
	defer b.abort()
	if n, err := b.Write([]byte("abcdefgh")); n != 8 || err != nil {
		t.Fatalf("write: %d %v", n, err)
	}
	written := make(chan error, 1)
	go func() { _, err := b.Write([]byte("ijkl")); written <- err }()
	select {
	case err := <-written:
		t.Fatalf("full buffer did not block: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	p, err := b.read(context.Background(), 3)
	if err != nil || !bytes.Equal(p, []byte("defgh")) {
		t.Fatalf("replay: %q %v", p, err)
	}
	if _, _, err := b.state(4, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ACK did not unblock writer")
	}
	if _, _, err := b.state(3, false); !errors.Is(err, errRXOffset) {
		t.Fatal("old replay not rejected", err)
	}
	if _, _, err := b.state(13, true); !errors.Is(err, errRXOffset) {
		t.Fatal("future ACK not rejected", err)
	}
	if _, _, err := b.state(2, true); err != nil {
		t.Fatal("old ACK must be harmless", err)
	}
	var replay []byte
	for off := uint64(4); off < 12; {
		p, err := b.read(context.Background(), off)
		if err != nil {
			t.Fatal(err)
		}
		replay = append(replay, p...)
		off += uint64(len(p))
	}
	if string(replay) != "efghijkl" {
		t.Fatalf("wrap corrupted bytes: %q", replay)
	}
}

func TestRXBufferFinishAndAbortUnblock(t *testing.T) {
	for _, abort := range []bool{false, true} {
		b := newRXBuffer(4)
		b.Write([]byte("tail"))
		done := make(chan error, 1)
		go func() { _, err := b.Write([]byte("blocked")); done <- err }()
		if abort {
			b.abort()
		} else {
			b.Close()
		}
		select {
		case err := <-done:
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("write stuck after close")
		}
		if !abort {
			p, err := b.read(context.Background(), 0)
			if err != nil || string(p) != "tail" {
				t.Fatal("lost final bytes", err)
			}
			if _, err := b.read(context.Background(), 4); !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			b.abort()
		}
	}
}

func TestRXBufferReadCancellation(t *testing.T) {
	b := newRXBuffer(4)
	defer b.abort()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.read(ctx, 0); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read stuck")
	}
}

func TestRXBufferReadyRangeWrapAndOwnership(t *testing.T) {
	b := newRXBuffer(8)
	defer b.abort()
	b.Write([]byte("abcdefgh"))
	b.state(4, true)
	b.Write([]byte("ijkl"))
	p, err := b.readReady(4, 12)
	if err != nil || string(p) != "efghijkl" {
		t.Fatalf("wrapped ready range: %q %v", p, err)
	}
	b.state(12, true)
	b.Write([]byte("mnopqrst"))
	if string(p) != "efghijkl" {
		t.Fatal("ready range aliases replay storage")
	}
	for _, offsets := range [][2]uint64{{4, 12}, {12, 21}, {13, 12}} {
		if _, err := b.readReady(offsets[0], offsets[1]); !errors.Is(err, errRXOffset) {
			t.Fatalf("invalid ready range %v: %v", offsets, err)
		}
	}
	b.abort()
	if _, err := b.readReady(12, 12); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("aborted ready range", err)
	}
}

func TestRXBufferAllocatesForRetainedBytes(t *testing.T) {
	b := newRXBuffer(16 << 20)
	defer b.abort()
	if len(b.data) != 0 {
		t.Fatal("an empty session allocated its entire replay window", len(b.data))
	}
	prefix := bytes.Repeat([]byte("a"), 6000)
	tail := bytes.Repeat([]byte("bcdef"), 8000)
	b.Write(prefix)
	if len(b.data) > 16<<10 {
		t.Fatal("a small payload reserved excessive storage", len(b.data))
	}
	b.state(5000, true)
	b.Write(tail)
	want := append(append([]byte(nil), prefix[5000:]...), tail...)
	got, err := b.readReady(5000, uint64(len(prefix)+len(tail)))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("growth corrupted the retained wrapped range", len(got), err)
	}
	end, _, _ := b.state(uint64(len(prefix)+len(tail)), true)
	if len(b.data) != 0 {
		t.Fatal("fully acknowledged storage was retained", len(b.data))
	}
	if p, err := b.readReady(end, end); err != nil || len(p) != 0 {
		t.Fatal("empty acknowledged range", p, err)
	}
	b.Write([]byte("next"))
	got, err = b.readReady(end, end+4)
	if err != nil || string(got) != "next" {
		t.Fatal("reuse after release corrupted offsets", string(got), err)
	}
}

func BenchmarkRXBufferSmallSession(b *testing.B) {
	payload := make([]byte, 1024)
	b.ReportAllocs()
	for b.Loop() {
		buffer := newRXBuffer(16 << 20)
		buffer.Write(payload)
		buffer.state(uint64(len(payload)), true)
		buffer.abort()
	}
}
