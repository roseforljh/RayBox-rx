package proxy_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/app/dispatcher"
	"github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/inbound"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/app/proxyman/outbound"
	pnet "github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/protocol/tls/cert"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/serial"
	"github.com/roseforljh/RayBox-rx/internal/protocol/common/uuid"
	"github.com/roseforljh/RayBox-rx/internal/protocol/core"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/freedom"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless/inbound"
	"github.com/roseforljh/RayBox-rx/internal/protocol/proxy/vless/outbound"
	"github.com/roseforljh/RayBox-rx/internal/protocol/testing/servers/tcp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
	_ "github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tcp"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet/tls"
)

func TestVisionXUDPConcurrentAssociations(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	if err := echo.SetReadBuffer(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	go func() {
		var data [2048]byte
		for {
			n, peer, err := echo.ReadFromUDP(data[:])
			if err != nil {
				return
			}
			_, _ = echo.WriteToUDP(data[:n], peer)
		}
	}()
	certificate, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
	userID := uuid.New()
	id := userID.String()
	serverPort := tcp.PickPort()
	apps := func() []*serial.TypedMessage {
		return []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		}
	}
	server, err := core.New(&core.Config{
		App: apps(),
		Inbound: []*core.InboundHandlerConfig{{
			ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
				Listen:   pnet.NewIPOrDomain(pnet.LocalHostIP),
				PortList: &pnet.PortList{Range: []*pnet.PortRange{pnet.SinglePortRange(serverPort)}},
				StreamSettings: &internet.StreamConfig{ProtocolName: "tcp", SecurityType: serial.GetMessageType(&tls.Config{}), SecuritySettings: []*serial.TypedMessage{
					serial.ToTypedMessage(&tls.Config{Certificate: []*tls.Certificate{tls.ParseCertificate(certificate)}}),
				}},
			}),
			ProxySettings: serial.ToTypedMessage(&inbound.Config{Users: []*protocol.User{{Account: serial.ToTypedMessage(&vless.Account{Id: id, Flow: vless.XRV})}}}),
		}},
		Outbound: []*core.OutboundHandlerConfig{{ProxySettings: serial.ToTypedMessage(&freedom.Config{
			FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
		})}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	client, err := core.New(&core.Config{
		App: apps(),
		Outbound: []*core.OutboundHandlerConfig{{
			ProxySettings: serial.ToTypedMessage(&outbound.Config{Vnext: &protocol.ServerEndpoint{
				Address: pnet.NewIPOrDomain(pnet.LocalHostIP), Port: uint32(serverPort), User: &protocol.User{Account: serial.ToTypedMessage(&vless.Account{Id: id, Flow: vless.XRV})},
			}}),
			SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{
				MultiplexSettings: &proxyman.MultiplexingConfig{Enabled: true, Concurrency: -1, XudpConcurrency: 128},
				StreamSettings: &internet.StreamConfig{ProtocolName: "tcp", SecurityType: serial.GetMessageType(&tls.Config{}), SecuritySettings: []*serial.TypedMessage{
					serial.ToTypedMessage(&tls.Config{ServerName: "localhost", PinnedPeerCertSha256: [][]byte{hash[:]}}),
				}},
			}),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	// Dispatch each association directly, avoiding a second local UDP ingress
	// socket dropping the initial 120 KB burst before XUDP sees it.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	destination := pnet.UDPDestination(pnet.LocalHostIP, pnet.Port(echo.LocalAddr().(*net.UDPAddr).Port))
	const sessions = 100
	start := make(chan struct{})
	errs := make(chan error, sessions)
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			associationCtx, closeAssociation := context.WithCancel(ctx)
			defer closeAssociation()
			conn, err := core.Dial(associationCtx, client, destination)
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			<-start
			payload := bytes.Repeat([]byte{byte(i + 1)}, 1200)
			if _, err := conn.Write(payload); err != nil {
				errs <- err
				return
			}
			response := make([]byte, 2048)
			n, err := conn.Read(response)
			if err != nil {
				errs <- err
			} else if !bytes.Equal(response[:n], payload) {
				errs <- fmt.Errorf("association %d response changed: %d bytes", i, n)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	failures := 0
	for err := range errs {
		failures++
		t.Error(err)
	}
	t.Logf("Vision+XUDP replies=%d/%d payload=%d bytes", sessions-failures, sessions, 1200)
}
