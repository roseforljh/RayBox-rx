package xray

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	xws "github.com/xtls/xray-core/transport/internet/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebSocketShortResponse(t *testing.T) {
	const response = "HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\ndual-core"
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		conn.WriteMessage(websocket.BinaryMessage, append([]byte{0, 0}, []byte(response)...))
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""), time.Now().Add(time.Second))
	}))
	defer fixture.Close()
	port := M.ParseSocksaddr(fixture.Listener.Addr().String()).Port
	config, err := buildConfig(json.RawMessage(`{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":` + fmtInt(int(port)) + `,"users":[{"id":"` + testUUID + `","encryption":"none"}]}]},"streamSettings":{"network":"ws"}}`))
	if err != nil {
		t.Fatal(err)
	}
	out := &Outbound{config: config, dialer: &testDialer{}}
	scope := adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())
	defer scope.Close()
	if err = out.Start(adapter.StartStateStart, scope); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		conn, err := out.DialContext(context.Background(), "tcp", M.ParseSocksaddr("127.0.0.1:80"))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))
		received := make([]byte, len(response))
		_, err = io.ReadFull(conn, received)
		conn.Close()
		if err != nil || string(received) != response {
			t.Fatalf("response: %q, %v", received, err)
		}
	}
}

// A normal close must drain already received bytes. Abnormal close codes must
// still be observable, rather than being silently converted to success.
func TestWebSocketCloseSemantics(t *testing.T) {
	for _, code := range []int{websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.ClosePolicyViolation} {
		t.Run(fmtInt(code), func(t *testing.T) {
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				conn.WriteMessage(websocket.BinaryMessage, []byte("response before close"))
				conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""), time.Now().Add(time.Second))
			}))
			defer fixture.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+fixture.URL[4:], nil)
			if err != nil {
				t.Fatal(err)
			}
			stream := xws.NewConnection(conn, conn.RemoteAddr(), nil, 0)
			defer stream.Close()
			received, err := io.ReadAll(stream)
			if string(received) != "response before close" {
				t.Fatalf("lost response: %q", received)
			}
			if code == websocket.ClosePolicyViolation {
				if !websocket.IsCloseError(err, code) {
					t.Fatalf("abnormal close hidden: %v", err)
				}
			} else if err != nil {
				t.Fatalf("normal close is not EOF: %v", err)
			}
		})
	}
}
