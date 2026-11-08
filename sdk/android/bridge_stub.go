//go:build !linux && !darwin

package raybox

import E "github.com/sagernet/sing/common/exceptions"

func NewBridgeService(options *BridgeOptions) (BridgeSession, error) {
	return nil, E.New("bridge service not supported on this platform")
}
