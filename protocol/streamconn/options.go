// Package streamconn connects RayBox protocol options to its session handlers.
package streamconn

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/roseforljh/RayBox-rx/adapter"
	"github.com/roseforljh/RayBox-rx/log"
	"github.com/roseforljh/RayBox-rx/option"
	"github.com/roseforljh/RayBox-rx/protocol/session"
)

func UsesXHTTP(transport *option.V2RayTransportOptions) bool {
	return transport != nil && transport.Type == "xhttp"
}

func NewVLESS(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.VLESSOutboundOptions) (adapter.Outbound, error) {
	if options.PacketEncoding != nil && *options.PacketEncoding != "" && *options.PacketEncoding != "xudp" {
		return nil, fmt.Errorf("VLESS session does not support packet_encoding=%q", *options.PacketEncoding)
	}
	encryption := options.Encryption
	if encryption == "" {
		encryption = "none"
	}
	settings := map[string]any{"address": options.Server, "port": options.ServerPort,
		"id": options.UUID, "flow": options.Flow, "encryption": encryption}
	packetEncoding := options.PacketEncoding
	if packetEncoding == nil {
		defaultEncoding := "xudp"
		packetEncoding = &defaultEncoding
	}
	return newOutbound(ctx, router, logger, tag, "vless", options.DialerOptions, options.Network,
		settings, options.TLS, options.Transport, options.Multiplex, packetEncoding)
}

func NewVMess(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.VMessOutboundOptions) (adapter.Outbound, error) {
	if options.AlterId != 0 || options.GlobalPadding || options.AuthenticatedLength {
		return nil, fmt.Errorf("VMess session does not support alter_id, global_padding or authenticated_length")
	}
	if options.PacketEncoding != "" && options.PacketEncoding != "xudp" {
		return nil, fmt.Errorf("VMess session does not support packet_encoding=%q", options.PacketEncoding)
	}
	security := options.Security
	switch security {
	case "":
		security = "auto"
	case "auto", "aes-128-gcm", "chacha20-poly1305":
	default:
		return nil, fmt.Errorf("VMess session does not support security=%q", security)
	}
	settings := map[string]any{"address": options.Server, "port": options.ServerPort,
		"id": options.UUID, "security": security}
	return newOutbound(ctx, router, logger, tag, "vmess", options.DialerOptions, options.Network,
		settings, options.TLS, options.Transport, options.Multiplex, &options.PacketEncoding)
}

func NewTrojan(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TrojanOutboundOptions) (adapter.Outbound, error) {
	settings := map[string]any{"address": options.Server, "port": options.ServerPort, "password": options.Password}
	return newOutbound(ctx, router, logger, tag, "trojan", options.DialerOptions, options.Network,
		settings, options.TLS, options.Transport, options.Multiplex, nil)
}

func newOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag, protocol string,
	dialer option.DialerOptions, network option.NetworkList, settings map[string]any, tls *option.OutboundTLSOptions,
	transport *option.V2RayTransportOptions, multiplex *option.OutboundMultiplexOptions, packetEncoding *string,
) (adapter.Outbound, error) {
	if multiplex != nil && multiplex.Enabled {
		return nil, fmt.Errorf("session transport cannot use h2mux/smux/yamux multiplex")
	}
	stream, err := buildStream(tls, transport)
	if err != nil {
		return nil, err
	}
	// The inherited stream struct emits nil fields as JSON null. Do not expose
	// its socket override key at all: socket policy belongs to the RayBox dialer.
	streamJSON, err := json.Marshal(stream)
	if err != nil {
		return nil, err
	}
	var streamFields map[string]json.RawMessage
	if err = json.Unmarshal(streamJSON, &streamFields); err != nil {
		return nil, err
	}
	delete(streamFields, "sockopt")
	raw, err := json.Marshal(map[string]any{"protocol": protocol, "settings": settings, "streamSettings": streamFields})
	if err != nil {
		return nil, err
	}
	return session.NewOutbound(ctx, router, logger, tag, session.Options{
		DialerOptions: dialer, Outbound: raw, Protocol: protocol, Network: network, PacketEncoding: packetEncoding,
	})
}
