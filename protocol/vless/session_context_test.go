package vless

import (
	"context"
	"crypto/x509"
	"io"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/adapter"
	outboundadapter "github.com/roseforljh/RayBox-rx/adapter/outbound"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
)

type sessionCertificateStore struct{ pool *x509.CertPool }

func (sessionCertificateStore) Name() string                                   { return "session-test-ca" }
func (sessionCertificateStore) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (s sessionCertificateStore) Pool() *x509.CertPool                         { return s.pool }
func (sessionCertificateStore) ExclusiveAnchors() bool                         { return true }

type sessionTimeService struct{ now time.Time }

func (s sessionTimeService) TimeFunc() func() time.Time {
	return func() time.Time { return s.now }
}

func TestStandardVLESSXHTTPTLSContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port, certificate := startSessionTestServer(t, ctx)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(certificate)) {
		t.Fatal("test certificate is invalid")
	}
	protected := &sessionTestDetour{
		Adapter: outboundadapter.NewAdapter("test", "protected", []string{"tcp"}, nil), port: port,
	}
	ctx = service.ContextWith[adapter.OutboundManager](ctx, sessionTestManager{detour: protected})
	ctx = service.ContextWith[adapter.CertificateStore](ctx, sessionCertificateStore{pool: roots})
	for _, test := range []struct {
		name       string
		now        time.Time
		success    bool
		explicitCA bool
	}{{"trusted-context", time.Now(), true, false},
		{"expired-at-context-time", time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC), false, false},
		{"explicit-ca-overrides-context", time.Now(), true, true}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := service.ContextWith[ntp.TimeService](ctx, sessionTimeService{now: test.now})
			tlsOptions := &option.OutboundTLSOptions{Enabled: true, ServerName: "example.com"}
			if test.explicitCA {
				ctx = service.ContextWith[adapter.CertificateStore](ctx, sessionCertificateStore{pool: x509.NewCertPool()})
				tlsOptions.Certificate = []string{certificate}
			}
			logger := log.NewNOPFactory().Logger()
			client, err := NewOutbound(ctx, nil, logger, "context-tls", option.VLESSOutboundOptions{
				DialerOptions: option.DialerOptions{Detour: "protected"},
				ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: port}, UUID: sessionTestUUID,
				OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: tlsOptions},
				Transport: &option.V2RayTransportOptions{Type: "xhttp", XHTTPOptions: option.XHTTPOptions{
					Host: "example.com", Path: "/raybox-session", Mode: "packet-up",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			scope := adapter.NewScope(ctx, logger)
			defer scope.Close()
			if err = client.(adapter.Lifecycle).Start(adapter.StartStateStart, scope); err != nil {
				t.Fatal(err)
			}
			conn, err := client.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.org:443"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(4 * time.Second))
			message := "context-root-and-clock"
			if _, err = io.WriteString(conn, message); err == nil {
				output := make([]byte, len(message))
				_, err = io.ReadFull(conn, output)
				if err == nil && string(output) != message {
					t.Fatal("TLS echo payload changed")
				}
			}
			if test.success && err != nil {
				t.Fatal(err)
			}
			if !test.success && err == nil {
				t.Fatal("TLS ignored the expired context time")
			}
		})
	}
}
