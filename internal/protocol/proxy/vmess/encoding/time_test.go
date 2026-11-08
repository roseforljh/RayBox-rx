package encoding_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess/aead"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vmess/encoding"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
)

type sessionClock struct{ now time.Time }

func (c *sessionClock) TimeFunc() func() time.Time {
	return func() time.Time { return c.now }
}

func TestClientSessionUsesContextClock(t *testing.T) {
	clock := &sessionClock{now: time.Unix(1900000000, 0)}
	ctx := service.ContextWith[ntp.TimeService](context.Background(), clock)
	account := toAccount(&vmess.Account{Id: "11111111-1111-4111-8111-111111111111"})
	request := &protocol.RequestHeader{
		Version: 1, User: &protocol.MemoryUser{Account: account},
		Command: protocol.RequestCommandTCP, Address: net.DomainAddress("example.com"),
		Port: 443, Security: protocol.SecurityType_AES128_GCM,
	}
	ctx = internet.ContextWithOwner(context.Background(), ctx)
	client := encoding.NewClientSession(ctx, 0)
	clock.now = clock.now.Add(time.Hour)
	var output bytes.Buffer
	if err := client.EncodeRequestHeader(request, &output); err != nil {
		t.Fatal(err)
	}
	var authID [16]byte
	copy(authID[:], output.Bytes())
	key := account.(*vmess.MemoryAccount).ID.CmdKey()
	actual, _, _, _ := aead.NewAuthIDDecoder(key).Decode(authID)
	if actual != clock.now.Unix() {
		t.Fatalf("authentication timestamp = %d, want context clock %d", actual, clock.now.Unix())
	}
}
