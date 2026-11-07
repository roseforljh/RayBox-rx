//go:build !linux && !darwin

package raybox

import "os"

func SubscribeNeighborTable(_ NeighborUpdateListener) (*NeighborSubscription, error) {
	return nil, os.ErrInvalid
}
