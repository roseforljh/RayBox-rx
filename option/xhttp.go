package option

import "encoding/json"

// XHTTPOptions belongs to the RayBox transport model. Extra preserves the
// complete transport configuration, including explicitly enabled RX/OOB.
// As in the existing wire implementation, extra replaces non-identity fields.
type XHTTPOptions struct {
	Host    string            `json:"host,omitempty"`
	Path    string            `json:"path,omitempty"`
	Mode    string            `json:"mode,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Extra   json.RawMessage   `json:"extra,omitempty"`
}
