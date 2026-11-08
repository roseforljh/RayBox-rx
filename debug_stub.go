//go:build !(linux || darwin)

package raybox

func rusageMaxRSS() float64 {
	return -1
}
