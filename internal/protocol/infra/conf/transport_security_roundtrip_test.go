package conf

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
)

func TestREALITYClientJSONRoundTrip(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, fields := range []string{
		``, `,"dest":null`, `,"target":null`, `,"target":null,"dest":null`,
	} {
		t.Run(fields, func(t *testing.T) {
			data := []byte(`{"fingerprint":"chrome","serverName":"example.com","publicKey":"` + key +
				`","shortId":"1234"` + fields + `}`)
			var options REALITYConfig
			if err := json.Unmarshal(data, &options); err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 2; round++ {
				built, err := options.Build()
				if err != nil {
					t.Fatal(err)
				}
				client := built.(*reality.Config)
				if client.ServerName != "example.com" || len(client.PublicKey) != 32 ||
					len(client.PrivateKey) != 0 || client.ShortId[0] != 0x12 {
					t.Fatal("REALITY client parsed as a server or lost identity")
				}
				encoded, err := json.Marshal(options)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &options); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestREALITYServerClientVersionPolicy(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, version := range []string{"", "26.3.27"} {
		t.Run("min="+version, func(t *testing.T) {
			options := REALITYConfig{
				Dest: json.RawMessage(`"example.com:443"`), ServerNames: []string{"example.com"},
				PrivateKey: key, ShortIds: []string{""}, MinClientVer: version,
			}
			built, err := options.Build()
			if err != nil {
				t.Fatal(err)
			}
			minimum := built.(*reality.Config).MinClientVer
			if version == "" && len(minimum) != 0 {
				t.Fatalf("default must not impose a client version: %v", minimum)
			}
			if version != "" && (len(minimum) != 3 || minimum[0] != 26 || minimum[1] != 3 || minimum[2] != 27) {
				t.Fatalf("explicit minimum client version lost: %v", minimum)
			}
		})
	}
}
