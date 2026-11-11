package grpc

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	xnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/grpc/encoding"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/stat"
	gogrpc "google.golang.org/grpc"
)

func TestGRPCLoopback(t *testing.T) {
	for _, multi := range []bool{false, true} {
		name := "single"
		if multi {
			name = "multi"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			config := &Config{ServiceName: "loopback", MultiMode: multi}
			server := gogrpc.NewServer()
			handler := &Listener{ctx: ctx, handler: func(conn stat.Connection) {
				go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
			}}
			encoding.RegisterGRPCServiceServerX(server, handler, config.getServiceName(), config.getTunStreamName(), config.getTunMultiStreamName())
			go server.Serve(listener)
			defer server.Stop()
			stream := &internet.MemoryStreamConfig{ProtocolName: "grpc", ProtocolSettings: config}
			defer CloseStream(stream)
			dest, err := xnet.ParseDestination("tcp:" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			conn, err := Dial(ctx, dest, stream)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			payload := bytes.Repeat([]byte("grpc payload"), 1024)
			if _, err := conn.Write(payload); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, reply); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(reply, payload) {
				t.Fatal("payload changed")
			}
		})
	}
}
