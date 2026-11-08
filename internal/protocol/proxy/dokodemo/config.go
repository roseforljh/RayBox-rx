package dokodemo

import (
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
)

// GetPredefinedAddress returns the defined address from proto config. Null if address is not valid.
func (v *Config) GetPredefinedAddress() net.Address {
	addr := v.RewriteAddress.AsAddress()
	if addr == nil {
		return nil
	}
	return addr
}
