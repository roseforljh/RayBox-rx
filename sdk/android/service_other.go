//go:build !windows

package raybox

import "syscall"

func dup(fd int) (nfd int, err error) {
	return syscall.Dup(fd)
}
