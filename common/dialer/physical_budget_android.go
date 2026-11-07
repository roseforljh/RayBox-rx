//go:build android

package dialer

import (
	"os"

	"golang.org/x/sys/unix"
)

func readPhysicalFDSoftLimit() (physicalFDLimit, error) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return physicalFDLimit{}, err
	}
	return physicalFDLimit{
		softLimit: limit.Cur,
		infinite:  limit.Cur == unix.RLIM_INFINITY,
	}, nil
}

func readPhysicalProcessFDCount() (int, bool, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, true, err
	}
	return len(entries), true, nil
}
