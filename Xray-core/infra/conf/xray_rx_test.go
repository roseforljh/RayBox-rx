package conf

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/transport/internet/splithttp"
	"google.golang.org/protobuf/proto"
)

func TestXrayRXJSONAndProtobuf(t *testing.T) {
	var c SplitHTTPConfig
	if err := json.Unmarshal([]byte(`{"mode":"packet-up","extra":{"uplinkHTTPMethod":"GET","uplinkDataPlacement":"header","uplinkDataKey":"data","scMaxEachPostBytes":4096,"rx":{"enabled":true,"stallDetect":true,"maxReplayBytes":65536,"maxReconnects":4000,"maxResponseBytes":1024,"preconnects":2,"maxSegments":2097152,"maxConcurrentSegments":4}}}`), &c); err != nil {
		t.Fatal(err)
	}
	p, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip := new(splithttp.Config)
	if err := proto.Unmarshal(encoded, roundtrip); err != nil {
		t.Fatal(err)
	}
	n, err := roundtrip.NormalizeRX()
	if err != nil {
		t.Fatal(err)
	}
	if !n.Enabled || !n.StallDetect || n.MaxReplayBytes != 65536 || n.MaxReconnects != 4000 || n.ProbeIntervalMs != 1000 || n.MaxResponseBytes != 1024 || n.Preconnects != 2 || n.MaxSegments != 2097152 || n.MaxConcurrentSegments != 4 {
		t.Fatalf("RX lost during JSON/protobuf roundtrip: %v", n)
	}
	if roundtrip.UplinkHTTPMethod != "GET" || roundtrip.UplinkDataPlacement != "header" || roundtrip.UplinkDataKey != "data" {
		t.Fatal("existing GET Header configuration changed")
	}
}

func TestXrayRXInvalidConfig(t *testing.T) {
	for _, input := range []string{
		`{"rx":{"enabled":true}}`,
		`{"mode":"stream-one","rx":{"enabled":true}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":255}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":65537}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":8192,"maxReplayBytes":4096}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"preconnects":1}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":1024,"preconnects":5}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxSegments":1}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":1024,"maxSegments":16777217}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxConcurrentSegments":2}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":1024,"maxConcurrentSegments":1}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxResponseBytes":1024,"maxConcurrentSegments":257}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxReplayBytes":4096,"maxResponseBytes":1024,"maxConcurrentSegments":8}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxReplayBytes":-1}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxSessions":100000}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxReconnects":-2}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"maxReconnects":131073}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"probeIntervalMs":5000,"stallTimeoutMs":1000}}`,
		`{"mode":"packet-up","rx":{"enabled":true,"sessionTtlSecs":1}}`,
		`{"mode":"packet-up","headers":{"x-rx-token":"fixed"},"rx":{"enabled":true}}`,
		`{"mode":"packet-up","sessionIDPlacement":"header","sessionIDKey":"X-RX-Token","rx":{"enabled":true}}`,
	} {
		t.Run(input, func(t *testing.T) {
			var c SplitHTTPConfig
			if err := json.Unmarshal([]byte(input), &c); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Build(); err == nil {
				t.Fatal("unsafe or unsupported RX config accepted")
			}
		})
	}
}

func TestXrayRXDisabledCompatibility(t *testing.T) {
	for _, input := range []string{`{}`, `{"mode":"stream-one","rx":{"enabled":false}}`, `{"mode":"packet-up"}`} {
		var c SplitHTTPConfig
		if err := json.Unmarshal([]byte(input), &c); err != nil {
			t.Fatal(err)
		}
		p, err := c.Build()
		if err != nil {
			t.Fatal(err)
		}
		n, err := p.(*splithttp.Config).NormalizeRX()
		if err != nil || n != nil {
			t.Fatalf("RX must be opt-in: %v %v", n, err)
		}
	}
}

func TestXrayRXReconnectBudgetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int32
		want   int32
		valid  bool
	}{
		{"default", 0, 64, true},
		{"minimum", 1, 1, true},
		{"large-explicit", 131072, 131072, true},
		{"above-limit", 131073, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := SplitHTTPConfig{Mode: "packet-up", RX: &RXConfig{Enabled: true, MaxReconnects: tc.budget}}
			p, err := c.Build()
			if !tc.valid {
				if err == nil {
					t.Fatal("over-limit reconnect budget accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			n, err := p.(*splithttp.Config).NormalizeRX()
			if err != nil {
				t.Fatal(err)
			}
			if n.MaxReconnects != tc.want {
				t.Fatalf("reconnect budget: got %d, want %d", n.MaxReconnects, tc.want)
			}
		})
	}
}
