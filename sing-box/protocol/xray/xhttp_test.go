package xray

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
)

// Real XHTTP + verified TLS through the same protected dialer used on Android.
// The self-signed fixture is explicitly trusted only by this test client.
func TestXHTTPVerifiedTLSAndReload(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	cert := fixture.Certificate()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	keyDER, err := x509.MarshalPKCS8PrivateKey(fixture.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	fixture.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	startServer := func(xhttpSettings map[string]any) int {
		portListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := portListener.Addr().(*net.TCPAddr).Port
		portListener.Close()
		serverStream := map[string]any{"network": "xhttp", "security": "tls", "xhttpSettings": xhttpSettings, "tlsSettings": map[string]any{"alpn": []string{"h2", "http/1.1"}, "certificates": []any{map[string]any{"certificate": strings.Split(string(certPEM), "\n"), "key": strings.Split(string(keyPEM), "\n")}}}}
		raw, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": testUUID}}, "decryption": "none"}, "streamSettings": serverStream}}, "outbounds": []any{loopbackFreedom}})
		var config conf.Config
		if err = json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		built, err := config.Build()
		if err != nil {
			t.Fatal(err)
		}
		server, err := core.New(built)
		if err != nil {
			t.Fatal(err)
		}
		instanceDialers.Store(server, &testDialer{})
		t.Cleanup(func() { server.Close(); instanceDialers.Delete(server) })
		if err = server.Start(); err != nil {
			t.Fatal(err)
		}
		return port
	}
	autoPort := startServer(map[string]any{"path": "/xhttp-test", "host": "example.com", "mode": "auto"})
	// Xray-RX only accepts explicit packet-up on both ends; its resume and control requests must stay on the protected dialer.
	rxSettings := map[string]any{"path": "/xhttp-test", "host": "example.com", "mode": "packet-up", "extra": map[string]any{"rx": map[string]any{"enabled": true}}}
	rxPort := startServer(rxSettings)
	segmentSettings := map[string]any{"path": "/xhttp-test", "host": "example.com", "mode": "packet-up", "extra": map[string]any{"rx": map[string]any{"enabled": true, "maxResponseBytes": 1024, "preconnects": 2}}}
	segmentPort := startServer(segmentSettings)
	type xhttpCase struct {
		name          string
		port          int
		xhttpSettings map[string]any
	}
	cases := []xhttpCase{{"rx-packet-up", rxPort, rxSettings}, {"rx-segments", segmentPort, segmentSettings}}
	mss := 1440
	if runtime.GOOS == "windows" {
		mss = -1
	}
	cases = append(cases, xhttpCase{"oob-primary", autoPort, map[string]any{"host": "example.com", "path": "/xhttp-test", "mode": "packet-up", "extra": map[string]any{"oob": map[string]any{"enabled": true, "tcpMaxSeg": mss}}}})
	for _, mode := range []string{"packet-up", "stream-up", "stream-one", "auto"} {
		cases = append(cases, xhttpCase{mode, autoPort, map[string]any{"host": "example.com", "path": "/xhttp-test", "mode": mode}})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for reload := 0; reload < 2; reload++ {
				clientRaw, _ := json.Marshal(map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": c.port, "users": []any{map[string]any{"id": testUUID, "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "xhttp", "security": "tls", "xhttpSettings": c.xhttpSettings, "tlsSettings": map[string]any{"serverName": "example.com", "alpn": []string{"h2", "http/1.1"}, "disableSystemRoot": true, "certificates": []any{map[string]any{"usage": "verify", "certificate": strings.Split(string(certPEM), "\n")}}}}})
				clientConfig, err := buildConfig(clientRaw)
				if err != nil {
					t.Fatal(err)
				}
				protected := &testDialer{}
				client := &Outbound{config: clientConfig, dialer: protected}
				scope := adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())
				t.Cleanup(func() { scope.Close() })
				if err = client.Start(adapter.StartStateStart, scope); err != nil {
					t.Fatal(err)
				}
				conn, err := client.DialContext(context.Background(), "tcp", M.SocksaddrFromNet(listener.Addr()))
				if err != nil {
					t.Fatal(err)
				}
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				message := []byte("verified xhttp traffic after reload")
				if c.name == "rx-segments" {
					message = []byte(strings.Repeat("verified xhttp segment;", 200))
				}
				if _, err = conn.Write(message); err != nil {
					t.Fatal(err)
				}
				received := make([]byte, len(message))
				if _, err = io.ReadFull(conn, received); err != nil {
					t.Fatal(err)
				}
				if string(received) != string(message) {
					t.Fatal("traffic corrupted")
				}
				conn.Close()
				if protected.calls.Load() == 0 {
					t.Fatal("XHTTP bypassed protected dialer")
				}
				scope.Close()
			}
		})
	}
}
