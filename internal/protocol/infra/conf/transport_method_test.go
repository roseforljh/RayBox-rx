package conf

import (
	"encoding/json"
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/splithttp"
)

func TestSplitHTTPPacketDownConfig(t *testing.T) {
	for _, tc := range []struct {
		name, input      string
		wantErr, enabled bool
	}{
		{"default", `{}`, false, false},
		{"direct", `{"packetDown":{"keepAlive":true,"keepAlivePeriod":"1-2","keepAliveLength":"24-64"}}`, false, true},
		{"extra", `{"path":"/pd","extra":{"packetDown":{"keepAlive":true,"keepAlivePeriod":1}}}`, false, true},
		{"negative", `{"packetDown":{"keepAlivePeriod":-1}}`, true, false},
		{"zero-start", `{"packetDown":{"keepAlivePeriod":"0-2"}}`, true, false},
		{"negative-length", `{"packetDown":{"keepAliveLength":-1}}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c SplitHTTPConfig
			if err := json.Unmarshal([]byte(tc.input), &c); err != nil {
				t.Fatal(err)
			}
			got, err := c.Build()
			if tc.wantErr {
				if err == nil {
					t.Fatal("accepted invalid packetDown")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := got.(*splithttp.Config).PacketDown
			if (p != nil) != tc.enabled {
				t.Fatalf("packetDown=%v", p)
			}
			if p != nil && !p.KeepAlive {
				t.Fatal("keepAlive dropped")
			}
		})
	}
}
