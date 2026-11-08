package hysteria2

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/roseforljh/RayBox-rx/adapter"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

type contractHTTPTransport struct{}

func (contractHTTPTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("contract transport is not for requests")
}

func (contractHTTPTransport) CloseIdleConnections() {}
func (contractHTTPTransport) Reset()                {}

type contractHTTPClientManager struct{}

func (contractHTTPClientManager) ResolveTransport(context.Context, logger.ContextLogger, option.HTTPClientOptions) (adapter.HTTPTransport, error) {
	return contractHTTPTransport{}, nil
}

func (contractHTTPClientManager) DefaultTransport() adapter.HTTPTransport {
	return contractHTTPTransport{}
}

func (contractHTTPClientManager) ResetNetwork() {}

func TestOutboundAndroidJSONContract(t *testing.T) {
	ctx := service.ContextWith[adapter.HTTPClientManager](context.Background(), contractHTTPClientManager{})
	content := []byte(`{
		"hop_interval": "1s",
		"hop_interval_max": "2s",
		"bbr_profile": "standard",
		"disable_chrome_parrot": true,
		"obfs": {
			"type": "gecko",
			"password": "gecko-password",
			"min_packet_size": 1200,
			"max_packet_size": 1300
		},
		"tls": {
			"enabled": true,
			"server_name": "realm.example.test",
			"insecure": true
		},
		"realm": {
			"server_url": "https://realm.example.test/control",
			"token": "realm-token",
			"realm_id": "test-realm",
			"stun_servers": ["127.0.0.1:3478"],
			"ip_version": 4,
			"http_client": {"version": 2}
		}
	}`)
	var options option.Hysteria2OutboundOptions
	if err := json.Unmarshal(content, &options); err != nil {
		t.Fatal(err)
	}
	if options.Server != "" || options.ServerPort != 0 || len(options.ServerPorts) != 0 {
		t.Fatalf("unexpected server fields: %+v", options.ServerOptions)
	}
	if options.HopIntervalMax == 0 || options.BBRProfile != "standard" || !options.DisableChromeParrot {
		t.Fatalf("snake_case fields not decoded: %+v", options)
	}
	if options.Obfs == nil || options.Obfs.Type != "gecko" || options.Obfs.GeckoOptions.MinPacketSize != 1200 {
		t.Fatalf("flat Gecko object not decoded: %+v", options.Obfs)
	}
	if options.Realm == nil || options.Realm.HTTPClient == nil || options.Realm.HTTPClient.Version != 2 {
		t.Fatalf("nested Realm object not decoded: %+v", options.Realm)
	}
	outbound, err := NewOutbound(ctx, nil, log.NewNOPFactory().Logger(), "android-contract", options)
	if err != nil {
		t.Fatal(err)
	}
	if keeper, ok := outbound.(adapter.IdleConnectionKeeper); ok {
		keeper.CloseIdleConnections()
	}
}

func TestOutboundAndroidJSONRealmServerConflicts(t *testing.T) {
	for _, field := range []string{"server", "server_port", "server_ports"} {
		t.Run(field, func(t *testing.T) {
			content := []byte(`{"server_url":"https://realm.example.test","realm_id":"test-realm","stun_servers":["127.0.0.1:3478"]}`)
			var realm option.Hysteria2Realm
			if err := json.Unmarshal(content, &realm); err != nil {
				t.Fatal(err)
			}
			options := option.Hysteria2OutboundOptions{
				Realm: &realm,
				OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
					TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "realm.example.test", Insecure: true},
				},
			}
			switch field {
			case "server":
				options.Server = "127.0.0.1"
			case "server_port":
				options.ServerPort = 443
			case "server_ports":
				options.ServerPorts = []string{"443:444"}
			}
			if _, err := NewOutbound(context.Background(), nil, log.NewNOPFactory().Logger(), "android-contract", options); err == nil {
				t.Fatalf("expected Realm conflict for %s", field)
			}
		})
	}
}
