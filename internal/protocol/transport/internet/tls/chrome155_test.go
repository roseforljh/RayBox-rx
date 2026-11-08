package tls

import (
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	utls "github.com/xtls/utls"
)

func TestChrome155Fingerprint(t *testing.T) {
	for _, name := range []string{"", "chrome", "hellochrome_auto", "chrome_155", "hellochrome_155"} {
		t.Run(name, func(t *testing.T) {
			id := GetFingerprint(name)
			if id == nil || *id != utls.HelloChrome_155 {
				t.Fatalf("fingerprint %q = %v, want Chrome 155", name, id)
			}
			conn := utls.UClient(&net.TCPConn{}, &utls.Config{ServerName: "example.com"}, *id)
			if err := conn.BuildHandshakeState(); err != nil {
				t.Fatal(err)
			}
			var anchors, hybrid bool
			for _, extension := range conn.Extensions {
				switch value := extension.(type) {
				case *utls.TrustAnchorsExtension:
					anchors = len(value.TrustAnchorIDs) == 28
				case *utls.KeyShareExtension:
					for _, share := range value.KeyShares {
						if share.Group == utls.X25519MLKEM768 {
							hybrid = len(share.Data) == 1216
						}
					}
				}
			}
			if !anchors || !hybrid {
				t.Fatalf("missing Chrome155 capabilities: anchors=%v, hybrid=%v", anchors, hybrid)
			}
		})
	}
	if id := GetFingerprint("hellochrome_133"); id == nil || *id != utls.HelloChrome_133 {
		t.Fatal("explicit Chrome133 fingerprint changed")
	}
}

func TestChrome155VerifiedHandshake(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, trusted := range []bool{true, false} {
		t.Run(map[bool]string{true: "trusted", false: "untrusted"}[trusted], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
			config := &stdtls.Config{ServerName: "example.com", RootCAs: x509.NewCertPool()}
			if trusted {
				config.RootCAs = roots
			}
			conn := UClient(raw, config, GetFingerprint("chrome")).(*UConn)
			err = conn.HTTPHandshakeContext(ctx, "http/1.1")
			if !trusted {
				if err == nil {
					t.Fatal("untrusted certificate accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadAll(conn); err != nil {
				t.Fatal(err)
			}
		})
	}
}
