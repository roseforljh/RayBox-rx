//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package splithttp

import (
	"fmt"
	"syscall"
)

func sendUrgent(syscall.RawConn, []byte) (int, error) {
	return 0, fmt.Errorf("TCP MSG_OOB is not supported on this platform")
}
