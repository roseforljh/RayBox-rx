package conf_test

import (
	"testing"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	. "github.com/roseforljh/RayBox-rx/internal/protocol/infra/conf"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/dokodemo"
)

func TestDokodemoConfig(t *testing.T) {
	creator := func() Buildable {
		return new(DokodemoConfig)
	}

	runMultiTestCase(t, []TestCase{
		{
			Input: `{
				"address": "8.8.8.8",
				"port": 53,
				"network": "tcp",
				"followRedirect": true,
				"userLevel": 1
			}`,
			Parser: loadJSON(creator),
			Output: &dokodemo.Config{
				RewriteAddress: &net.IPOrDomain{
					Address: &net.IPOrDomain_Ip{
						Ip: []byte{8, 8, 8, 8},
					},
				},
				RewritePort:     53,
				AllowedNetworks: []net.Network{net.Network_TCP},
				FollowRedirect:  true,
				UserLevel:       1,
			},
		},
	})
}
