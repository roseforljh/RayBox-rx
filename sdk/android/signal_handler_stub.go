//go:build !darwin || !badlinkname

package raybox

func PrepareCrashSignalHandlers() {}

func ReinstallCrashSignalHandlers() {}
