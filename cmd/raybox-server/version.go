package main

import (
	"fmt"

	"github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/base"
	C "github.com/roseforljh/RayBox-rx/constant"
)

var cmdVersion = &base.Command{
	UsageLine: "{{.Exec}} version",
	Short:     "Show current version of RayBox",
	Long: `Version prints the build information for RayBox executables.
	`,
	Run: executeVersion,
}

func executeVersion(cmd *base.Command, args []string) {
	printVersion()
}

func printVersion() {
	fmt.Println("RayBox version", C.Version)
}
