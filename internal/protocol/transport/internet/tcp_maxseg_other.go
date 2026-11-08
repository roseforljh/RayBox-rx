//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package internet

import "fmt"

func SetTCPMaxSegment(uintptr, int) error {
	return fmt.Errorf("TCP_MAXSEG is unsupported on this platform")
}
