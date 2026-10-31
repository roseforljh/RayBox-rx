package splithttp

import (
	"golang.org/x/sys/windows"
	"syscall"
)

func sendUrgent(raw syscall.RawConn, p []byte) (n int, err error) {
	var sent uint32
	pollErr := raw.Write(func(fd uintptr) bool {
		b := windows.WSABuf{Len: uint32(len(p)), Buf: &p[0]}
		err = windows.WSASend(windows.Handle(fd), &b, 1, &sent, 1 /* MSG_OOB */, nil, nil)
		return err != windows.WSAEWOULDBLOCK
	})
	if pollErr != nil {
		return int(sent), pollErr
	}
	return int(sent), err
}
