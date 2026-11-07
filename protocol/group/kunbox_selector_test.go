package group

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/roseforljh/RayBox-rx/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

type testKunBoxGroupOutbound struct {
	tag          string
	outboundType string
	now          string
	children     []string
}

func (o *testKunBoxGroupOutbound) Type() string         { return o.outboundType }
func (o *testKunBoxGroupOutbound) Tag() string          { return o.tag }
func (*testKunBoxGroupOutbound) Network() []string      { return []string{"tcp"} }
func (*testKunBoxGroupOutbound) Dependencies() []string { return nil }
func (*testKunBoxGroupOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (*testKunBoxGroupOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}
func (o *testKunBoxGroupOutbound) Now() string   { return o.now }
func (o *testKunBoxGroupOutbound) All() []string { return o.children }
func (o *testKunBoxGroupOutbound) Selected(string) adapter.Outbound { return nil }
func (o *testKunBoxGroupOutbound) AttachConnection(io.Closer) func() { return func() {} }

var _ adapter.OutboundGroup = (*testKunBoxGroupOutbound)(nil)

type testKunBoxLeafOutbound struct {
	tag          string
	outboundType string
}

func (o *testKunBoxLeafOutbound) Type() string         { return o.outboundType }
func (o *testKunBoxLeafOutbound) Tag() string          { return o.tag }
func (*testKunBoxLeafOutbound) Network() []string      { return []string{"tcp"} }
func (*testKunBoxLeafOutbound) Dependencies() []string { return nil }
func (*testKunBoxLeafOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (*testKunBoxLeafOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}

func TestKunBoxFallbackSelectorIsEphemeral(t *testing.T) {
	require.True(t, isKunBoxEphemeralSelector("F:bd61a3c4-5786-3f85-a174-a5fbe5be01b6"))
	require.False(t, isKunBoxEphemeralSelector("F:node-id"))
	require.False(t, isKunBoxEphemeralSelector("PROXY"))
	require.False(t, isKunBoxEphemeralSelector("P:profile"))
}

func TestKunBoxRemoteOnlyGroupChecksEveryReachableLeaf(t *testing.T) {
	root := &testKunBoxGroupOutbound{tag: "root", outboundType: "urltest", children: []string{"nested"}}
	nested := &testKunBoxGroupOutbound{tag: "nested", outboundType: "selector", children: []string{"proxy"}}
	proxy := &testKunBoxLeafOutbound{tag: "proxy", outboundType: "vless"}
	direct := &testKunBoxLeafOutbound{tag: "direct", outboundType: "direct"}
	outbounds := map[string]adapter.Outbound{"nested": nested, "proxy": proxy, "direct": direct}
	lookup := func(tag string) (adapter.Outbound, bool) { outbound, loaded := outbounds[tag]; return outbound, loaded }

	require.True(t, isKunBoxRemoteOnlyOutbound(root, lookup))
	nested.children = []string{"direct"}
	require.False(t, isKunBoxRemoteOnlyOutbound(root, lookup))
	nested.children = []string{"root"}
	outbounds["root"] = root
	require.False(t, isKunBoxRemoteOnlyOutbound(root, lookup))
}
