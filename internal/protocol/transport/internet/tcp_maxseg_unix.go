//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package internet

import "golang.org/x/sys/unix"

// SetTCPMaxSegment must run before connect, so the SYN advertises the limit.
func SetTCPMaxSegment(fd uintptr, size int) error {
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_MAXSEG, size)
}
