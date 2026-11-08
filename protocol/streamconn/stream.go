package streamconn

import (
	stdtls "crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/infra/conf"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func buildStream(tls *option.OutboundTLSOptions, transport *option.V2RayTransportOptions) (*conf.StreamConfig, error) {
	stream := &conf.StreamConfig{}
	if err := buildTLS(stream, tls); err != nil {
		return nil, err
	}
	if transport == nil || transport.Type == "" {
		return stream, nil
	}
	network := conf.TransportProtocol(transport.Type)
	stream.Network = &network
	switch transport.Type {
	case "xhttp":
		x := transport.XHTTPOptions
		stream.XHTTPSettings = &conf.SplitHTTPConfig{Host: x.Host, Path: x.Path, Mode: x.Mode,
			Headers: x.Headers, Extra: append(json.RawMessage(nil), x.Extra...)}
	case "ws":
		ws := transport.WebsocketOptions
		if ws.MaxEarlyData != 0 && ws.EarlyDataHeaderName != "Sec-WebSocket-Protocol" {
			return nil, fmt.Errorf("session WebSocket early data requires Sec-WebSocket-Protocol")
		}
		headers, err := singleHeaders(ws.Headers)
		if err != nil {
			return nil, err
		}
		path := ws.Path
		if ws.MaxEarlyData != 0 {
			u, err := url.Parse(path)
			if err != nil {
				return nil, err
			}
			query := u.Query()
			query.Set("ed", strconv.FormatUint(uint64(ws.MaxEarlyData), 10))
			u.RawQuery = query.Encode()
			path = u.String()
		}
		stream.WSSettings = &conf.WebSocketConfig{Path: path, Headers: headers}
	case "httpupgrade":
		u := transport.HTTPUpgradeOptions
		headers, err := singleHeaders(u.Headers)
		if err != nil {
			return nil, err
		}
		stream.HTTPUPGRADESettings = &conf.HttpUpgradeConfig{Host: u.Host, Path: u.Path, Headers: headers}
	case "grpc":
		g := transport.GRPCOptions
		idle, err := seconds(g.IdleTimeout)
		if err != nil {
			return nil, err
		}
		ping, err := seconds(g.PingTimeout)
		if err != nil {
			return nil, err
		}
		if g.ForceLite {
			return nil, fmt.Errorf("session gRPC does not support force_lite")
		}
		stream.GRPCSettings = &conf.GRPCConfig{ServiceName: g.ServiceName, IdleTimeout: idle,
			HealthCheckTimeout: ping, PermitWithoutStream: g.PermitWithoutStream}
	default:
		return nil, fmt.Errorf("session transport does not support %q", transport.Type)
	}
	return stream, nil
}

func singleHeaders(headers badoption.HTTPHeader) (map[string]string, error) {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) != 1 {
			return nil, fmt.Errorf("session transport requires one value for header %q", key)
		}
		result[key] = values[0]
	}
	return result, nil
}

func seconds(value badoption.Duration) (int32, error) {
	d := time.Duration(value)
	if d < 0 || d%time.Second != 0 || d/time.Second > 1<<31-1 {
		return 0, fmt.Errorf("session gRPC timeout must be a nonnegative whole number of seconds")
	}
	return int32(d / time.Second), nil
}

func buildTLS(stream *conf.StreamConfig, tls *option.OutboundTLSOptions) error {
	if tls == nil || !tls.Enabled {
		return nil
	}
	// Reject every nonzero unmapped option, rather than silently weakening TLS.
	allowed := strings.Fields("enabled engine server_name alpn min_version max_version cipher_suites curve_preferences certificate certificate_path client_certificate client_certificate_path client_key client_key_path utls reality")
	if tls.Reality != nil && tls.Reality.Enabled {
		allowed = strings.Fields("enabled engine server_name utls reality")
	}
	data, err := json.Marshal(tls)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		found := false
		for _, supported := range allowed {
			found = found || key == supported
		}
		if !found {
			return fmt.Errorf("session TLS does not support %s", key)
		}
	}
	if tls.Engine != "" && tls.Engine != "go" {
		return fmt.Errorf("session TLS does not support engine=%q", tls.Engine)
	}
	versions := map[string]int{"": 0, "1.0": 10, "1.1": 11, "1.2": 12, "1.3": 13}
	minimum, minOK := versions[tls.MinVersion]
	maximum, maxOK := versions[tls.MaxVersion]
	if !minOK || !maxOK || maximum != 0 && minimum > maximum {
		return fmt.Errorf("invalid session TLS version range: %q to %q", tls.MinVersion, tls.MaxVersion)
	}
	for _, name := range tls.CipherSuites {
		valid := false
		for _, cipher := range append(stdtls.CipherSuites(), stdtls.InsecureCipherSuites()...) {
			valid = valid || cipher.Name == name
		}
		if !valid {
			return fmt.Errorf("unknown session TLS cipher_suite: %s", name)
		}
	}
	fingerprint := "hellogolang"
	if tls.UTLS != nil && tls.UTLS.Enabled {
		fingerprint = tls.UTLS.Fingerprint
		if fingerprint == "" {
			fingerprint = "chrome"
		}
	}
	if tls.Reality != nil && tls.Reality.Enabled {
		stream.Security = "reality"
		stream.REALITYSettings = &conf.REALITYConfig{ServerName: tls.ServerName, Fingerprint: fingerprint,
			PublicKey: tls.Reality.PublicKey, ShortId: tls.Reality.ShortID}
		return nil
	}
	stream.Security = "tls"
	config := &conf.TLSConfig{ServerName: tls.ServerName, Fingerprint: fingerprint,
		MinVersion: tls.MinVersion, MaxVersion: tls.MaxVersion, CipherSuites: strings.Join(tls.CipherSuites, ":")}
	if len(tls.ALPN) != 0 {
		alpn := conf.StringList(append([]string(nil), tls.ALPN...))
		config.ALPN = &alpn
	}
	if len(tls.CurvePreferences) != 0 {
		curves := conf.StringList{}
		for _, curve := range tls.CurvePreferences {
			encoded, err := json.Marshal(curve)
			if err != nil {
				return err
			}
			var name string
			if err := json.Unmarshal(encoded, &name); err != nil {
				return err
			}
			switch name {
			case "P256", "P384", "P521":
				name = "Curve" + name
			}
			curves = append(curves, name)
		}
		config.CurvePreferences = &curves
	}
	if len(tls.Certificate) != 0 || tls.CertificatePath != "" {
		config.DisableSystemRoot = true
		config.Certs = append(config.Certs, &conf.TLSCertConfig{Usage: "verify",
			CertStr: tls.Certificate, CertFile: tls.CertificatePath})
	}
	if len(tls.ClientCertificate) != 0 || tls.ClientCertificatePath != "" || len(tls.ClientKey) != 0 || tls.ClientKeyPath != "" {
		config.Certs = append(config.Certs, &conf.TLSCertConfig{Usage: "encipherment", OneTimeLoading: true,
			CertStr: tls.ClientCertificate, CertFile: tls.ClientCertificatePath,
			KeyStr: tls.ClientKey, KeyFile: tls.ClientKeyPath})
	}
	stream.TLSSettings = config
	return nil
}
