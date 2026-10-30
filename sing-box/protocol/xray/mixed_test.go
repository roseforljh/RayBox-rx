package xray_test

import (
	"bufio"
	"context"
	"fmt"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMixedWebSocketShortResponse(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		io.WriteString(w, "dual-core")
	}))
	defer target.Close()
	freePort := func() int {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	ws, p := freePort(), freePort()
	ctx := include.Context(context.Background())
	raw := fmt.Sprintf(`{"log":{"level":"debug"},"inbounds":[{"type":"mixed","tag":"mixed","listen":"127.0.0.1","listen_port":%d},{"type":"vless","tag":"server","listen":"127.0.0.1","listen_port":%d,"users":[{"uuid":"11111111-1111-4111-8111-111111111111"}],"transport":{"type":"ws","path":"/test"}}],"outbounds":[{"type":"xray","tag":"client","outbound":{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":"11111111-1111-4111-8111-111111111111","encryption":"none"}]}]},"streamSettings":{"network":"ws","wsSettings":{"path":"/test"}}}},{"type":"direct","tag":"direct"}],"route":{"rules":[{"inbound":"server","outbound":"direct"},{"inbound":"mixed","outbound":"client"}]}}`, p, ws, ws)
	var config option.Options
	if err := json.UnmarshalContext(ctx, []byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	b, err := box.New(box.Options{Options: config, Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.Start(); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(c, "GET %s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.URL, target.Listener.Addr())
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.Close()
		if resp.StatusCode != 200 || string(body) != "dual-core" {
			t.Fatalf("%s %s", resp.Status, body)
		}
	}
}
