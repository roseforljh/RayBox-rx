//go:build !android

package dialer

func readPhysicalFDSoftLimit() (physicalFDLimit, error) {
	return physicalFDLimit{infinite: true}, nil
}

func readPhysicalProcessFDCount() (int, bool, error) {
	return 0, false, nil
}
