//go:build !(darwin || linux)

package raybox

import "os"

func getTunnelName(fd int32) (string, error) {
	return "", os.ErrInvalid
}
