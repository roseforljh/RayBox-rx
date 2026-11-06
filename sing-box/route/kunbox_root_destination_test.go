package route

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

type testKunBoxOutbound struct {
	outboundType string
}

func (o *testKunBoxOutbound) Type() string         { return o.outboundType }
func (*testKunBoxOutbound) Tag() string            { return "test" }
func (*testKunBoxOutbound) Network() []string      { return []string{N.NetworkTCP} }
func (*testKunBoxOutbound) Dependencies() []string { return nil }
func (*testKunBoxOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (*testKunBoxOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}

type testKunBoxRemoteOnlyOutbound struct {
	*testKunBoxOutbound
	remoteOnly bool
}

func (o *testKunBoxRemoteOnlyOutbound) KunBoxRemoteOnly() bool { return o.remoteOnly }

func TestApplyKunBoxProxyDestinationOverride(t *testing.T) {
	original := M.Socksaddr{Addr: netip.MustParseAddr("108.160.162.109"), Port: 443}
	metadata := adapter.InboundContext{
		Inbound: "redirect-in-v4", Network: N.NetworkTCP, Destination: original, Domain: "graph.instagram.com",
	}
	metadata.DestinationAddresses = []netip.Addr{original.Addr}

	require.True(t, applyKunBoxProxyDestinationOverride(&metadata, &testKunBoxOutbound{C.TypeVLESS}))
	require.Equal(t, original, metadata.RouteOriginalDestination)
	require.Equal(t, "graph.instagram.com", metadata.Destination.Fqdn)
	require.Empty(t, metadata.DestinationAddresses)
}

func TestApplyKunBoxProxyDestinationOverrideSupportsAppRouteLanes(t *testing.T) {
	for _, inbound := range []string{"root-lane-003-tcp-v4", "root-lane-003-tcp-v6"} {
		t.Run(inbound, func(t *testing.T) {
			original := M.Socksaddr{Addr: netip.MustParseAddr("199.59.150.43"), Port: 443}
			metadata := adapter.InboundContext{
				Inbound: inbound, Network: N.NetworkTCP, Destination: original, Domain: "iapi.example.com",
			}

			require.True(t, applyKunBoxProxyDestinationOverride(&metadata, &testKunBoxOutbound{C.TypeVLESS}))
			require.Equal(t, original, metadata.RouteOriginalDestination)
			require.Equal(t, "iapi.example.com", metadata.Destination.Fqdn)
			require.Empty(t, metadata.DestinationAddresses)
		})
	}
}

func TestApplyKunBoxProxyDestinationOverrideRejectsUnsafePaths(t *testing.T) {
	base := adapter.InboundContext{
		Inbound: "redirect-in-v4", Network: N.NetworkTCP,
		Destination: M.Socksaddr{Addr: netip.MustParseAddr("108.160.162.109"), Port: 443},
		Domain:      "graph.instagram.com",
	}
	for _, outboundType := range []string{"", C.TypeDirect, C.TypeBlock, C.TypeDNS, C.TypeSelector, C.TypeURLTest} {
		metadata := base
		require.False(t, applyKunBoxProxyDestinationOverride(&metadata, &testKunBoxOutbound{outboundType}))
		require.True(t, metadata.Destination.IsIP())
	}
	for name, mutate := range map[string]func(*adapter.InboundContext){
		"unsupported inbound": func(metadata *adapter.InboundContext) { metadata.Inbound = "mixed-in" },
		"udp":              func(metadata *adapter.InboundContext) { metadata.Network = N.NetworkUDP },
		"non web port":     func(metadata *adapter.InboundContext) { metadata.Destination.Port = 22 },
		"missing domain":   func(metadata *adapter.InboundContext) { metadata.Domain = "" },
		"explicit override": func(metadata *adapter.InboundContext) {
			metadata.RouteOriginalDestination = metadata.Destination
		},
	} {
		t.Run(name, func(t *testing.T) {
			metadata := base
			mutate(&metadata)
			require.False(t, applyKunBoxProxyDestinationOverride(&metadata, &testKunBoxOutbound{C.TypeVLESS}))
			require.True(t, metadata.Destination.IsIP())
		})
	}
}

func TestApplyKunBoxProxyDestinationOverrideSupportsVpnIPv6HTTP(t *testing.T) {
	metadata := adapter.InboundContext{
		Inbound: "tun-in", Network: N.NetworkTCP,
		Destination: M.Socksaddr{Addr: netip.MustParseAddr("2001:db8::1"), Port: 80}, Domain: "example.com",
	}
	require.True(t, applyKunBoxProxyDestinationOverride(&metadata, &testKunBoxOutbound{C.TypeTrojan}))
	require.Equal(t, "example.com:80", metadata.Destination.String())
}

func TestApplyKunBoxProxyDestinationOverrideSupportsRemoteOnlyURLTest(t *testing.T) {
	base := adapter.InboundContext{
		Inbound: "redirect-in-v4", Network: N.NetworkTCP,
		Destination: M.Socksaddr{Addr: netip.MustParseAddr("108.160.162.109"), Port: 443},
		Domain:      "graph.instagram.com",
	}
	remoteOnly := &testKunBoxRemoteOnlyOutbound{
		testKunBoxOutbound: &testKunBoxOutbound{C.TypeURLTest},
		remoteOnly:         true,
	}
	mixed := &testKunBoxRemoteOnlyOutbound{
		testKunBoxOutbound: &testKunBoxOutbound{C.TypeURLTest},
		remoteOnly:         false,
	}

	metadata := base
	require.True(t, applyKunBoxProxyDestinationOverride(&metadata, remoteOnly))
	metadata = base
	require.False(t, applyKunBoxProxyDestinationOverride(&metadata, mixed))
}
