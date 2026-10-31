package internet

import "golang.org/x/sys/windows"

func SetTCPMaxSegment(fd uintptr, size int) error {
	return windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_TCP, 4 /* TCP_MAXSEG */, size)
}
