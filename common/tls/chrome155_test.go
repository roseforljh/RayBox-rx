//go:build with_utls

package tls

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	utls "github.com/metacubex/utls"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/sagernet/sing/common/logger"
)

func TestNativeChrome155Fingerprint(t *testing.T) {
	for _, name := range []string{"", "chrome", "chrome_155", "hellochrome_155"} {
		t.Run(name, func(t *testing.T) {
			id, err := uTLSClientHelloID(name)
			if err != nil || id != utls.HelloChrome_155 {
				t.Fatalf("fingerprint %q = %v, %v; want Chrome155", name, id, err)
			}
		})
	}
}

func TestNativeChrome155VerifiedHandshake(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	for _, trusted := range []bool{true, false} {
		t.Run(map[bool]string{true: "trusted", false: "untrusted"}[trusted], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			options := option.OutboundTLSOptions{
				ServerName: "example.com", ALPN: []string{"http/1.1"},
				UTLS: &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
			}
			if trusted {
				options.Certificate = []string{certificate}
			}
			config, err := NewUTLSClient(ctx, logger.NOP(), "example.com", options)
			if err != nil {
				t.Fatal(err)
			}
			if !trusted {
				config.(*UTLSClientConfig).config.RootCAs = x509.NewCertPool()
			}
			raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
			_, err = ClientHandshake(ctx, raw, config)
			if trusted && err != nil || !trusted && err == nil {
				t.Fatalf("trusted=%v handshake error=%v", trusted, err)
			}
		})
	}
}
