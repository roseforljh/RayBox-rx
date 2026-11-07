package all

import (
	// The following are necessary as they register handlers in their init functions.

	// Mandatory features. Can't remove unless there are replacements.
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/dispatcher"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/inbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/outbound"

	// Default commander and all its services. This is an optional feature.
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/commander"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/log/command"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/command"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/stats/command"

	// Developer preview services
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/observatory/command"

	// Other optional features.
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/dns"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/dns/fakedns"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/geodata"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/log"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/metrics"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/policy"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/reverse"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/router"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/stats"

	// Fix dependency cycle caused by core import in internet package
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tagged/taggedimpl"

	// Developer preview features
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/observatory"

	// Inbound and outbound proxies.
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/blackhole"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/dns"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/dokodemo"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/freedom"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/http"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/loopback"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/masque"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/shadowsocks"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/socks"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/trojan"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless/inbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess/inbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess/outbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/proxy/wireguard"

	// Transports
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/grpc"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/httpupgrade"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/kcp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/masque"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/reality"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/splithttp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tcp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/udp"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/websocket"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/xdrive"

	// Transport headers
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/headers/http"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/headers/noop"

	// JSON & TOML & YAML
	_ "github.com/roseforljh/RayBox-rx/cmd/raybox-server/json"
	_ "github.com/roseforljh/RayBox-rx/cmd/raybox-server/toml"
	_ "github.com/roseforljh/RayBox-rx/cmd/raybox-server/yaml"

	// Load config from file or http(s)
	_ "github.com/roseforljh/RayBox-rx/cmd/raybox-server/confloader/external"

	// Commands
	_ "github.com/roseforljh/RayBox-rx/cmd/raybox-server/commands/all"
)
