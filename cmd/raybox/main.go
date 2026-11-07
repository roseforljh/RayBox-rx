//go:build !generate_completions

package main

import "github.com/roseforljh/RayBox-rx/log"

func main() {
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
