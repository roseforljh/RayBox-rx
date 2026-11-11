package buf_test

import (
	"crypto/tls"
	"io"
	"testing"
	"time"

	. "github.com/roseforljh/RayBox-rx/internal/protocol/common/buf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/testing/servers/tcp"
)

func TestTimeoutWrapperReaderTimeoutAndResume(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	reader := &TimeoutWrapperReader{Reader: NewReader(r)}
	for range 2 {
		mb, err := reader.ReadMultiBufferTimeout(time.Millisecond)
		if err != ErrReadTimeout || !mb.IsEmpty() {
			t.Fatalf("timeout returned %v, %v", mb, err)
		}
	}
	go func() { _, _ = w.Write([]byte("late payload")) }()
	mb, err := reader.ReadMultiBufferTimeout(time.Second)
	defer ReleaseMulti(mb)
	if err != nil || mb.Len() != 12 || string(mb[0].Bytes()) != "late payload" {
		t.Fatalf("pending read lost payload: %v, %v", mb, err)
	}
}

func TestWriterCreation(t *testing.T) {
	tcpServer := tcp.Server{}
	dest, err := tcpServer.Start()
	if err != nil {
		t.Fatal("failed to start tcp server: ", err)
	}
	defer tcpServer.Close()

	conn, err := net.Dial("tcp", dest.NetAddr())
	if err != nil {
		t.Fatal("failed to dial a TCP connection: ", err)
	}
	defer conn.Close()

	{
		writer := NewWriter(conn)
		if _, ok := writer.(*BufferToBytesWriter); !ok {
			t.Fatal("writer is not a BufferToBytesWriter")
		}

		writer2 := NewWriter(writer.(io.Writer))
		if writer2 != writer {
			t.Fatal("writer is not reused")
		}
	}

	tlsConn := tls.Client(conn, &tls.Config{
		InsecureSkipVerify: true,
	})
	defer tlsConn.Close()

	{
		writer := NewWriter(tlsConn)
		if _, ok := writer.(*SequentialWriter); !ok {
			t.Fatal("writer is not a SequentialWriter")
		}
	}
}
