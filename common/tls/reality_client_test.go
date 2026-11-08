//go:build with_utls

package tls

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	stdtls "crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	protocolnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	protocolreality "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/sagernet/sing/common/logger"
	"github.com/xtls/reality"
)

// Exercise the actual strict Xray REALITY server, not just a supported-groups declaration.
func TestREALITYClientHybridHandshake(t *testing.T) {
	for _, group := range []stdtls.CurveID{stdtls.X25519MLKEM768, stdtls.X25519} {
		for _, client := range []string{"native", "session"} {
			t.Run(client+"/"+group.String(), func(t *testing.T) {
				testREALITYClientHybridHandshake(t, client, group)
			})
		}
	}
}

func testREALITYClientHybridHandshake(t *testing.T, client string, group stdtls.CurveID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target := httptest.NewUnstartedServer(nil)
	target.TLS = &stdtls.Config{CurvePreferences: []stdtls.CurveID{group}, MinVersion: stdtls.VersionTLS13}
	target.StartTLS()
	defer target.Close()
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := &reality.Config{
		Type: "tcp", Dest: target.Listener.Addr().String(),
		ServerNames: map[string]bool{"example.com": true}, PrivateKey: privateKey,
		ShortIds: map[[8]byte]bool{{}: true}, SessionTicketsDisabled: true,
		DialContext: (&net.Dialer{Timeout: time.Second}).DialContext,
	}
	cacheKey := config.Dest + " example.com 2"
	reality.GlobalPostHandshakeRecordsLens.Store(cacheKey, []int{})
	defer reality.GlobalPostHandshakeRecordsLens.Delete(cacheKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		raw, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer raw.Close()
		_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
		conn, handshakeErr := reality.Server(ctx, raw, config)
		if handshakeErr != nil {
			serverDone <- handshakeErr
			return
		}
		_, echoErr := io.CopyN(conn, conn, 32)
		serverDone <- echoErr
	}()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	var conn net.Conn
	var negotiatedGroup uint16
	if client == "native" {
		options := option.OutboundTLSOptions{
			ServerName: "example.com", UTLS: &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
			Reality: &option.OutboundRealityOptions{
				Enabled: true, PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
			},
		}
		var native Config
		native, err = NewRealityClient(ctx, logger.NOP(), "example.com", options)
		if err == nil {
			conn, err = native.(*RealityClientConfig).ClientHandshake(ctx, raw)
			if err == nil {
				negotiatedGroup = uint16(conn.(*realityClientConnWrapper).HandshakeState.ServerHello.ServerShare.Group)
			}
		}
	} else {
		conn, err = protocolreality.UClient(raw, &protocolreality.Config{
			Fingerprint: "chrome", ServerName: "example.com", PublicKey: privateKey.PublicKey().Bytes(),
		}, ctx, protocolnet.TCPDestination(protocolnet.DomainAddress("example.com"), 443))
		if err == nil {
			negotiatedGroup = uint16(conn.(*protocolreality.UConn).HandshakeState.ServerHello.ServerShare.Group)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if negotiatedGroup != uint16(group) {
		t.Fatalf("negotiated group %d, want %v", negotiatedGroup, group)
	}
	payload := bytes.Repeat([]byte{0x5a}, 32)
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, reply) {
		t.Fatal("REALITY payload changed")
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}
