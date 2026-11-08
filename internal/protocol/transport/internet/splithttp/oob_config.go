package splithttp

import (
	"fmt"
	"net"
	"slices"

	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
)

// NormalizeOOB also validates configurations supplied directly as protobufs.
func (c *Config) NormalizeOOB() (*OOBConfig, error) {
	o := c.GetOob()
	if !o.GetEnabled() {
		return nil, nil
	}
	if c.Mode != "packet-up" || c.DownloadSettings != nil || c.GetRx().GetEnabled() {
		return nil, fmt.Errorf("XHTTP OOB requires packet-up, no downloadSettings, and RX disabled on the primary")
	}
	n := &OOBConfig{Enabled: true, Positions: slices.Clone(o.Positions), Addresses: slices.Clone(o.Addresses), FallbackSettings: o.FallbackSettings, HttpVersions: slices.Clone(o.HttpVersions)}
	if len(n.HttpVersions) == 0 {
		n.HttpVersions = []string{"h2", "http/1.1"}
	}
	if len(n.HttpVersions) > 2 {
		return nil, fmt.Errorf("OOB httpVersions allows only h2 and http/1.1")
	}
	for i, v := range n.HttpVersions {
		if (v != "h2" && v != "http/1.1") || slices.Contains(n.HttpVersions[:i], v) {
			return nil, fmt.Errorf("OOB httpVersions must be unique h2 or http/1.1")
		}
	}
	if len(n.Positions) == 0 {
		n.Positions = []int32{1, 0}
	}
	if len(n.Positions) > 2 {
		return nil, fmt.Errorf("XHTTP OOB positions allows only 0 (before ClientHello) and 1 (after its first byte)")
	}
	for i, pos := range n.Positions {
		if pos < 0 || pos > 1 || slices.Contains(n.Positions[:i], pos) {
			return nil, fmt.Errorf("XHTTP OOB positions must be unique 0 or 1")
		}
	}
	for _, f := range []struct {
		name                  string
		value, def, low, high int32
		dst                   *int32
	}{
		{"delayMs", o.DelayMs, 5, 1, 50, &n.DelayMs},
		{"maxAttempts", o.MaxAttempts, 4, 1, 8, &n.MaxAttempts},
		{"attemptTimeoutMs", o.AttemptTimeoutMs, 2500, 100, 10000, &n.AttemptTimeoutMs},
		{"tcpMaxSeg", o.TcpMaxSeg, 1440, 536, 1440, &n.TcpMaxSeg},
	} {
		v := f.value
		if v == 0 {
			v = f.def
		}
		if f.name == "tcpMaxSeg" && v == -1 {
			*f.dst = v // Explicit opt-out for systems with read-only TCP_MAXSEG.
			continue
		}
		if v < f.low || v > f.high {
			return nil, fmt.Errorf("XHTTP OOB %s must be in [%d,%d]", f.name, f.low, f.high)
		}
		*f.dst = v
	}
	if len(n.Addresses) > 64 {
		return nil, fmt.Errorf("XHTTP OOB allows at most 64 addresses")
	}
	for i, address := range n.Addresses {
		if net.ParseIP(address) == nil || slices.Contains(n.Addresses[:i], address) {
			return nil, fmt.Errorf("XHTTP OOB addresses must be unique IP literals")
		}
	}
	if f := n.FallbackSettings; f != nil {
		if f.GetEffectiveProtocol() != "splithttp" {
			return nil, fmt.Errorf("OOB fallbackSettings requires XHTTP RX")
		}
		settings, err := f.GetEffectiveTransportSettings()
		if err != nil {
			return nil, err
		}
		fallback, ok := settings.(*Config)
		if !ok || fallback.GetOob().GetEnabled() || !fallback.GetRx().GetEnabled() {
			return nil, fmt.Errorf("OOB fallbackSettings must enable RX and must not enable OOB")
		}
		if _, err := fallback.NormalizeRX(); err != nil {
			return nil, err
		}
		if _, err := internet.ToMemoryStreamConfig(f); err != nil {
			return nil, err
		}
	}
	return n, nil
}
