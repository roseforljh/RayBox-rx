// Package core provides protocol runtime compatibility APIs used by RayBox.
package core

import (
	"runtime"
	"runtime/debug"

	C "github.com/roseforljh/RayBox-rx/constant"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/serial"
)

var (
	Version_x byte = 26
	Version_y byte = 10
	Version_z byte = 31
)

var (
	build = "Custom"
)

func init() {
	// Manually injected
	if build != "Custom" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	var isDirty bool
	var foundBuild bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if len(setting.Value) < 7 {
				return
			}
			build = setting.Value[:7]
			foundBuild = true
		case "vcs.modified":
			isDirty = setting.Value == "true"
		}
	}
	if isDirty && foundBuild {
		build += "-dirty"
	}
}

func Version() string {
	return C.Version
}

// VersionStatement returns RayBox build information.
func VersionStatement() []string {
	return []string{
		serial.Concat("RayBox ", Version(), " ", build, " (", runtime.Version(), " ", runtime.GOOS, "/", runtime.GOARCH, ")"),
		"RayBox protocol runtime.",
	}
}
