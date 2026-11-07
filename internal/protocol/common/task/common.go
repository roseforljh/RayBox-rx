package task

import "github.com/roseforljh/RayBox-rx/internal/protocol/common"

// Close returns a func() that closes v.
func Close(v interface{}) func() error {
	return func() error {
		return common.Close(v)
	}
}
