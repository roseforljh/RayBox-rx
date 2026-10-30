package splithttp

import (
	"fmt"
	"net"
	"strings"
)

// NormalizeRX validates bounds also for callers constructing protobuf configs
// directly (rather than going through the JSON config builder).
func (c *Config) NormalizeRX() (*RXConfig, error) {
	if c.GetRx() == nil || !c.Rx.Enabled {
		return nil, nil
	}
	if c.Mode != "packet-up" {
		return nil, fmt.Errorf("Xray-RX requires explicit packet-up mode")
	}
	if c.DownloadSettings != nil {
		return nil, fmt.Errorf("Xray-RX does not yet support downloadSettings")
	}
	// Metadata and user-supplied headers must never overwrite negotiation or
	// capability headers. These names belong exclusively to the RX protocol.
	for key := range c.Headers {
		if strings.HasPrefix(strings.ToLower(key), "x-rx-") {
			return nil, fmt.Errorf("Xray-RX reserves X-RX-* headers")
		}
	}
	for _, key := range []string{c.SessionIDKey, c.SeqKey, c.UplinkDataKey, c.XPaddingHeader, c.XPaddingKey} {
		if strings.HasPrefix(strings.ToLower(key), "x-rx-") {
			return nil, fmt.Errorf("Xray-RX metadata keys must not use X-RX-* names")
		}
	}
	r := c.Rx
	if _, err := rxDecodeAuthKey(r.AuthKey); err != nil {
		return nil, err
	}
	n := &RXConfig{Enabled: true, StallDetect: r.StallDetect, AuthKey: r.AuthKey}
	fields := []struct {
		name                  string
		value, def, low, high int32
		dst                   *int32
	}{
		{"maxReplayBytes", r.MaxReplayBytes, 262144, 4096, 16 << 20, &n.MaxReplayBytes},
		{"maxSessions", r.MaxSessions, 128, 1, 4096, &n.MaxSessions},
		{"probeIntervalMs", r.ProbeIntervalMs, 1000, 10, 30000, &n.ProbeIntervalMs},
		{"stallTimeoutMs", r.StallTimeoutMs, 3000, 10, 120000, &n.StallTimeoutMs},
		{"requestTimeoutMs", r.RequestTimeoutMs, 5000, 10, 30000, &n.RequestTimeoutMs},
		{"sessionTtlSecs", r.SessionTtlSecs, 30, 1, 300, &n.SessionTtlSecs},
		{"maxReconnects", r.MaxReconnects, 64, 1, 131072, &n.MaxReconnects},
		{"retryDelayMs", r.RetryDelayMs, 100, 1, 5000, &n.RetryDelayMs},
	}
	for _, f := range fields {
		v := f.value
		if v == 0 {
			v = f.def
		}
		if v < f.low || v > f.high {
			return nil, fmt.Errorf("Xray-RX %s must be in [%d,%d] (0 selects default)", f.name, f.low, f.high)
		}
		*f.dst = v
	}
	if r.MaxResponseBytes != 0 && (r.MaxResponseBytes < 256 || r.MaxResponseBytes > min(65536, n.MaxReplayBytes)) {
		return nil, fmt.Errorf("Xray-RX maxResponseBytes must be 0 or in [256,min(65536,maxReplayBytes)]")
	}
	if r.Preconnects < 0 || r.Preconnects > 4 || (r.Preconnects != 0 && r.MaxResponseBytes == 0) {
		return nil, fmt.Errorf("Xray-RX preconnects must be in [0,4] and requires maxResponseBytes")
	}
	if r.MaxSegments < 0 || r.MaxSegments > 16777216 || (r.MaxSegments != 0 && r.MaxResponseBytes == 0) {
		return nil, fmt.Errorf("Xray-RX maxSegments requires maxResponseBytes and must be in [0,16777216]")
	}
	n.MaxResponseBytes, n.Preconnects, n.MaxSegments = r.MaxResponseBytes, r.Preconnects, r.MaxSegments
	if n.MaxResponseBytes != 0 && n.MaxSegments == 0 {
		n.MaxSegments = 2097152
	}
	if r.MaxConcurrentSegments != 0 && (r.MaxConcurrentSegments < 2 || r.MaxConcurrentSegments > 256 || n.MaxResponseBytes == 0 || uint64(r.MaxConcurrentSegments)*uint64(n.MaxResponseBytes) > uint64(n.MaxReplayBytes)) {
		return nil, fmt.Errorf("Xray-RX maxConcurrentSegments must be 0 or in [2,256], requires maxResponseBytes and their product <= maxReplayBytes")
	}
	n.MaxConcurrentSegments = r.MaxConcurrentSegments
	// Some paths drop new TCP connections per destination IP; anycast CDNs serve
	// the same host on other IPs, so new RX connections may rotate over them.
	if len(r.SpreadAddresses) > 64 || (len(r.SpreadAddresses) != 0 && n.MaxResponseBytes == 0) {
		return nil, fmt.Errorf("Xray-RX spreadAddresses allows at most 64 IP literals and requires maxResponseBytes")
	}
	for _, address := range r.SpreadAddresses {
		if net.ParseIP(address) == nil {
			return nil, fmt.Errorf("Xray-RX spreadAddresses entry %q is not an IP literal", address)
		}
	}
	n.SpreadAddresses = r.SpreadAddresses
	if n.StallTimeoutMs < n.ProbeIntervalMs {
		return nil, fmt.Errorf("Xray-RX stallTimeoutMs must be >= probeIntervalMs")
	}
	if n.SessionTtlSecs*1000 <= n.RequestTimeoutMs+2*n.ProbeIntervalMs {
		return nil, fmt.Errorf("Xray-RX sessionTtlSecs must exceed request timeout plus two probe intervals")
	}
	return n, nil
}

func (c *Config) normalizedRX() (*RXConfig, error) { return c.NormalizeRX() }
