//go:build !linux

package main

import "github.com/roseforljh/RayBox-rx/option"

func runInUserNamespaceIfNeeded(options option.Options, optionsList []*OptionsEntry) error {
	return nil
}
