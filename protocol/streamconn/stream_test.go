package streamconn

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/infra/conf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/splithttp"
	"github.com/roseforljh/RayBox-rx/option"
)

func TestXHTTPPreservesExtraAndDefaults(t *testing.T) {
	for _, extra := range []string{"", `{"headers":{"X-Test":"extra"},"h2Params":{"maxConcurrentStreams":12}}`} {
		transport := &option.V2RayTransportOptions{Type: "xhttp", XHTTPOptions: option.XHTTPOptions{
			Host: "example.com", Path: "/session", Mode: "packet-up", Headers: map[string]string{"X-Test": "outer"},
			Extra: json.RawMessage(extra),
		}}
		stream, err := buildStream(nil, transport)
		if err != nil {
			t.Fatal(err)
		}
		x := stream.XHTTPSettings
		if x.Host != "example.com" || x.Path != "/session" || x.Mode != "packet-up" || string(x.Extra) != extra {
			t.Fatal("XHTTP identity or extra changed")
		}
		if x.RX != nil || x.OOB != nil || stream.Security != "" {
			t.Fatal("optional RX/OOB/security enabled by default")
		}
		if _, err := stream.Build(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestXHTTPPacketDownAndXmuxReachRuntime(t *testing.T) {
	transport := &option.V2RayTransportOptions{Type: "xhttp", XHTTPOptions: option.XHTTPOptions{
		Host: "example.com", Path: "/session", Mode: "packet-up",
		Extra: json.RawMessage(`{"packetDown":{"keepAlive":true,"keepAlivePeriod":"10-20","keepAliveLength":"24-64"},"xmux":{"maxConcurrency":5,"maxConnections":0}}`),
	}}
	stream, err := buildStream(nil, transport)
	if err != nil {
		t.Fatal(err)
	}
	built, err := stream.Build()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := built.TransportSettings[0].Settings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	x := settings.(*splithttp.Config)
	if x.PacketDown == nil || !x.PacketDown.KeepAlive ||
		x.PacketDown.GetNormalizedKeepAlivePeriod().To != 20 ||
		x.PacketDown.GetNormalizedKeepAliveLength().From != 24 ||
		x.Xmux.MaxConcurrency.From != 5 || x.Xmux.MaxConnections.To != 0 {
		t.Fatal("Packet-Down or XMUX lost between native configuration and runtime")
	}
}

func TestSessionTLSRejectsUnmappedFields(t *testing.T) {
	for _, field := range []string{`"insecure":true`, `"disable_sni":true`, `"fragment":true`,
		`"certificate_public_key_sha256":["AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="]`,
		`"handshake_timeout":"5s"`, `"ech":{"enabled":true}`} {
		var tls option.OutboundTLSOptions
		if err := json.Unmarshal([]byte(`{"enabled":true,`+field+`}`), &tls); err != nil {
			t.Fatal(err)
		}
		if _, err := buildStream(&tls, nil); err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Fatalf("unmapped TLS field was discarded: %s, err=%v", field, err)
		}
	}
}

func TestSessionTLSRejectsInvalidParameters(t *testing.T) {
	for _, options := range []option.OutboundTLSOptions{
		{Enabled: true, MinVersion: "1.4"},
		{Enabled: true, MaxVersion: "1.1", MinVersion: "1.3"},
		{Enabled: true, CipherSuites: []string{"not-a-cipher"}},
		{Enabled: true, Engine: "unsupported"},
	} {
		if _, err := buildStream(&options, nil); err == nil {
			t.Fatalf("invalid TLS parameters accepted: %+v", options)
		}
	}
}

func TestSessionTLSPreservesVerificationAndReality(t *testing.T) {
	stream, err := buildStream(&option.OutboundTLSOptions{Enabled: true, ServerName: "example.com",
		MinVersion: "1.2", MaxVersion: "1.3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Security != "tls" || stream.TLSSettings.AllowInsecure || stream.TLSSettings.Fingerprint != "hellogolang" ||
		stream.TLSSettings.ServerName != "example.com" || stream.TLSSettings.MinVersion != "1.2" {
		t.Fatal("TLS verification or options changed")
	}
	if _, err := stream.Build(); err != nil {
		t.Fatal(err)
	}
	tls := &option.OutboundTLSOptions{Enabled: true, ServerName: "example.com",
		UTLS:    &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
		Reality: &option.OutboundRealityOptions{Enabled: true, PublicKey: strings.Repeat("A", 43), ShortID: "1234"}}
	stream, err = buildStream(tls, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stream.REALITYSettings.ServerName != tls.ServerName ||
		stream.REALITYSettings.PublicKey != tls.Reality.PublicKey || stream.REALITYSettings.ShortId != "1234" {
		t.Fatal("Reality settings changed")
	}
	if _, err := stream.Build(); err != nil {
		t.Fatal(err)
	}
	// The public adapter serializes the stream before the session parses it.
	// Absent RawMessage fields become JSON null, not server-side destinations.
	encoded, err := json.Marshal(stream)
	if err != nil {
		t.Fatal(err)
	}
	var decoded conf.StreamConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	built, err := decoded.Build()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := built.SecuritySettings[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	reality := settings.(*reality.Config)
	if reality.ServerName != tls.ServerName || len(reality.PublicKey) != 32 {
		t.Fatal("REALITY client identity lost after JSON round trip")
	}
}
