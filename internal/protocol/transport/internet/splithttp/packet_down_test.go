package splithttp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/signal/done"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
)

func pdTestConfig() *PacketDownConfig {
	return &PacketDownConfig{KeepAlive: true, KeepAlivePeriod: &RangeConfig{From: 1, To: 1}, KeepAliveLength: &RangeConfig{From: 32, To: 32}}
}

func TestPacketDownFrameRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	want := bytes.Repeat([]byte("data"), 17500)
	w := newPacketDownWriter(&wire, "abcdefgh1234", &PacketDownConfig{})
	if n, err := w.Write(want); err != nil || n != len(want) {
		t.Fatalf("write: %d %v", n, err)
	}
	w.Stop()
	r := newPacketDownReader(io.NopCloser(&wire))
	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero read: %d %v", n, err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("roundtrip: %d bytes, %v", len(got), err)
	}
}

func TestPacketDownMalformedFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"header", []byte{0, 1, 0}}, {"flags", []byte{4, 1, 0, 0}}, {"type", []byte{0, 3, 0, 0}},
		{"session", []byte{1, 1, 0, 0, 1}}, {"seq", []byte{2, 1, 0, 0, 1}}, {"payload", []byte{0, 1, 0, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pdReadFrame(bufio.NewReader(bytes.NewReader(tc.wire))); err == nil {
				t.Fatal("accepted malformed frame")
			}
		})
	}
}

type pdShortWriter struct{}

func (pdShortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestPacketDownShortWrite(t *testing.T) {
	w := newPacketDownWriter(pdShortWriter{}, "", &PacketDownConfig{})
	if _, err := w.Write([]byte("test")); err != io.ErrShortWrite {
		t.Fatalf("short write: %v", err)
	}
	if _, err := w.Write([]byte("more")); err != io.ErrShortWrite {
		t.Fatalf("corrupted stream reused: %v", err)
	}
}

func TestPacketDownConcurrentWrites(t *testing.T) {
	var wire bytes.Buffer
	w := newPacketDownWriter(&wire, "abcdefgh", &PacketDownConfig{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Write(bytes.Repeat([]byte("x"), 3000)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r := bufio.NewReader(&wire)
	for i := 0; i < 20; i++ {
		f, err := pdReadFrame(r)
		if err != nil || f.seq != uint64(i) || len(f.payload) != 3000 {
			t.Fatalf("frame %d: seq %d length %d err %v", i, f.seq, len(f.payload), err)
		}
	}
}

func TestPacketDownStopImmediate(t *testing.T) {
	var wire bytes.Buffer
	w := newPacketDownWriter(&wire, "", &PacketDownConfig{KeepAlive: true, KeepAlivePeriod: &RangeConfig{From: 3600, To: 3600}})
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the timer")
	}
	if wire.Len() != 0 {
		t.Fatal("padding written after Stop")
	}
	if _, err := w.Write([]byte("still valid")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("closed")); err != io.ErrClosedPipe {
		t.Fatalf("closed write: %v", err)
	}
}

func TestPacketDownCloseUnblocksWrite(t *testing.T) {
	writer, reader := stdnet.Pipe()
	defer reader.Close()
	w := newPacketDownWriter(writer, "", &PacketDownConfig{})
	writeDone := make(chan error, 1)
	go func() { _, err := w.Write([]byte("blocked")); writeDone <- err }()
	closed := make(chan struct{})
	go func() { _ = w.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked with Write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("write did not exit")
	}
}

func TestPacketDownHTTPShutdownBlockedWrite(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "disconnect"}[disconnect], func(t *testing.T) {
			started := make(chan struct{})
			finished := make(chan struct{})
			cancelWrite := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				stream := &httpServerConn{Instance: done.New(), Reader: req.Body, ResponseWriter: rw}
				w := newPacketDownWriter(stream, "abcdefgh", pdTestConfig())
				rw.WriteHeader(http.StatusOK)
				rw.(http.Flusher).Flush()
				close(started)
				writeDone := make(chan struct{})
				go func() { _, _ = w.Write(make([]byte, 32<<20)); close(writeDone) }()
				select {
				case <-cancelWrite:
				case <-req.Context().Done():
				}
				_ = w.Close()
				<-writeDone
				close(finished)
			}))
			defer server.Close()
			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			<-started
			if disconnect {
				_ = response.Body.Close()
			} else {
				close(cancelWrite)
				defer response.Body.Close()
			}
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP shutdown blocked behind writer")
			}
		})
	}
}

func TestPacketDownPaddingAndEmptyData(t *testing.T) {
	var wire bytes.Buffer
	for _, f := range []pdFrame{{typ: pdTypePadding, payload: []byte("padding")}, {typ: pdTypeData}, {typ: pdTypeData, payload: []byte("hello")}} {
		if err := pdWriteFrame(&wire, f); err != nil {
			t.Fatal(err)
		}
	}
	got, err := io.ReadAll(newPacketDownReader(io.NopCloser(&wire)))
	if err != nil || string(got) != "hello" {
		t.Fatalf("payload %q error %v", got, err)
	}
}

func TestPacketDownDefaults(t *testing.T) {
	c := &PacketDownConfig{}
	if p := c.GetNormalizedKeepAlivePeriod(); p.From != 10 || p.To != 30 {
		t.Fatal(p)
	}
	if p := c.GetNormalizedKeepAliveLength(); p.From != 24 || p.To != 64 {
		t.Fatal(p)
	}
	c.KeepAliveLength = &RangeConfig{From: 60000, To: 90000}
	if p := c.GetNormalizedKeepAliveLength(); p.From != 60000 || p.To != 65535 {
		t.Fatal(p)
	}
	fields := (&Config{}).ProtoReflect().Descriptor().Fields()
	for name, number := range map[string]int{"rx": 30, "oob": 31, "h2Params": 32, "packetDown": 33} {
		f := fields.ByJSONName(name)
		if f == nil || int(f.Number()) != number {
			t.Fatalf("field %s changed", name)
		}
	}
}

// A reverse proxy whose upstream socket closes after 1.5 seconds without bytes.
// Padding must keep this socket alive before the delayed application response.
type pdIdleConn struct{ stdnet.Conn }

func (c pdIdleConn) Read(b []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond)); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func TestPacketDownHTTPCompatibilityAndIdle(t *testing.T) {
	for _, tc := range []struct {
		name           string
		client, server bool
		delay          time.Duration
		want           bool
		stripHeader    bool
	}{
		{"both", true, true, 0, true, false}, {"old-server", true, false, 0, true, false}, {"old-client", false, true, 0, true, false},
		{"idle-keepalive", true, true, 2200 * time.Millisecond, true, false}, {"idle-disabled", false, false, 2200 * time.Millisecond, false, false},
		{"stripped-confirmation-is-incompatible", true, true, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			serverConfig := &Config{Path: "/pd", Mode: "packet-up"}
			if tc.server {
				serverConfig.PacketDown = pdTestConfig()
			}
			portListener, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := net.Port(portListener.Addr().(*stdnet.TCPAddr).Port)
			_ = portListener.Close()
			listener, err := ListenXH(ctx, net.LocalHostIP, port, &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: serverConfig}, func(conn stat.Connection) {
				go func() {
					defer conn.Close()
					select {
					case <-time.After(tc.delay):
					case <-ctx.Done():
						return
					}
					_, _ = conn.Write([]byte("delayed-data"))
				}()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			backend, _ := url.Parse("http://" + listener.Addr().String())
			proxy := httputil.NewSingleHostReverseProxy(backend)
			if tc.stripHeader {
				proxy.ModifyResponse = func(r *http.Response) error { r.Header.Del(pdResponseHeader); return nil }
			}
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (stdnet.Conn, error) {
				c, e := (&stdnet.Dialer{}).DialContext(ctx, network, address)
				if e != nil {
					return nil, e
				}
				return pdIdleConn{c}, nil
			}}
			defer transport.CloseIdleConnections()
			proxy.Transport = transport
			proxy.FlushInterval = -1
			front := httptest.NewServer(proxy)
			defer front.Close()
			clientConfig := &Config{Path: "/pd"}
			if tc.client {
				clientConfig.PacketDown = pdTestConfig()
			}
			client := &DefaultDialerClient{transportConfig: clientConfig, client: front.Client()}
			stream, _, _, err := client.OpenStream(ctx, front.URL+"/pd", "abcdefgh123456789", nil, false)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			got, err := io.ReadAll(stream)
			if tc.want {
				if err != nil || string(got) != "delayed-data" {
					t.Fatalf("payload %q, err %v", got, err)
				}
			} else if string(got) == "delayed-data" {
				t.Fatal("idle proxy unexpectedly survived without keepalive")
			}
		})
	}
}
