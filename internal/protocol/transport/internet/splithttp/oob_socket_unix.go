//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package splithttp

import (
	"golang.org/x/sys/unix"
	"syscall"
)

func sendUrgent(raw syscall.RawConn, p []byte) (n int, err error) {
	pollErr := raw.Write(func(fd uintptr) bool {
		for {
			n, err = unix.SendmsgN(int(fd), p, nil, nil, unix.MSG_OOB)
			if err != unix.EINTR {
				break
			}
		}
		return err != unix.EAGAIN && err != unix.EWOULDBLOCK
	})
	if pollErr != nil {
		return n, pollErr
	}
	return n, err
}
