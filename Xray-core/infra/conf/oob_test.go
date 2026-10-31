package conf

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/transport/internet/splithttp"
)

func TestXHTTPOOBConfig(t *testing.T) {
	var c SplitHTTPConfig
	if err := json.Unmarshal([]byte(`{"mode":"packet-up","extra":{"oob":{"enabled":true,"addresses":["192.0.2.1"],"positions":[1,0]}}}`), &c); err != nil {
		t.Fatal(err)
	}
	built, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	n, err := built.(*splithttp.Config).NormalizeOOB()
	if err != nil || n.TcpMaxSeg != 1440 || n.MaxAttempts != 4 || n.AttemptTimeoutMs != 2500 || n.DelayMs != 5 || len(n.Positions) != 2 {
		t.Fatal(n, err)
	}
	for _, raw := range []string{
		`{"mode":"auto","oob":{"enabled":true}}`,
		`{"mode":"packet-up","oob":{"enabled":true,"positions":[2]}}`,
		`{"mode":"packet-up","oob":{"enabled":true,"positions":[1,1]}}`,
		`{"mode":"packet-up","oob":{"enabled":true,"maxAttempts":9}}`,
		`{"mode":"packet-up","oob":{"enabled":true,"tcpMaxSeg":1460}}`,
		`{"mode":"packet-up","oob":{"enabled":true,"addresses":["example.com"]}}`,
		`{"mode":"packet-up","rx":{"enabled":true},"oob":{"enabled":true}}`,
		`{"mode":"packet-up","oob":{"enabled":true,"fallbackSettings":{"network":"xhttp","xhttpSettings":{"mode":"packet-up"}}}}`,
	} {
		var invalid SplitHTTPConfig
		if err := json.Unmarshal([]byte(raw), &invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := invalid.Build(); err == nil {
			t.Fatalf("accepted invalid OOB config: %s", raw)
		}
	}
}
