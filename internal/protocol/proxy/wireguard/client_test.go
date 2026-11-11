package wireguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/roseforljh/RayBox-rx/internal/protocol/common/net"
	"github.com/roseforljh/RayBox-rx/internal/protocol/features/dns"
	"github.com/roseforljh/RayBox-rx/internal/protocol/transport/internet"
)

type endpointDNS struct {
	dns.Client
	lookup func(string, dns.IPOption) ([]net.IP, uint32, error)
}

func (d endpointDNS) LookupIP(host string, opt dns.IPOption) ([]net.IP, uint32, error) {
	return d.lookup(host, opt)
}

func TestEndpointLookupDeadline(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := endpointDNS{lookup: func(string, dns.IPOption) ([]net.IP, uint32, error) {
		close(started)
		<-release
		defer close(finished)
		return []net.IP{net.ParseIP("192.0.2.1")}, 0, nil
	}}
	defer func() { close(release); <-finished }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	b := &bind{ctx: ctx, resolveFunc: MakeResolveLocal(d, &internet.MemoryStreamConfig{})}
	result := make(chan error, 1)
	go func() { _, err := b.ParseEndpoint("example.com:51820"); result <- err }()
	<-started
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("endpoint lookup ignored connection deadline")
	}
}

func TestEndpointLookupResults(t *testing.T) {
	wantErr := errors.New("DNS failure")
	for _, fail := range []bool{false, true} {
		d := endpointDNS{lookup: func(host string, opt dns.IPOption) ([]net.IP, uint32, error) {
			if host != "example.com" || !opt.IPv4Enable || !opt.IPv6Enable {
				t.Error("incorrect lookup")
			}
			if fail {
				return nil, 0, wantErr
			}
			return []net.IP{net.ParseIP("192.0.2.1")}, 0, nil
		}}
		resolve := MakeResolveLocal(d, &internet.MemoryStreamConfig{})
		ip, err := resolve(context.Background(), "example.com")
		if fail {
			if err != wantErr {
				t.Fatalf("lost DNS error: %v", err)
			}
		} else if err != nil || !ip.Equal(net.ParseIP("192.0.2.1")) {
			t.Fatalf("got %v, %v", ip, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := endpointDNS{lookup: func(string, dns.IPOption) ([]net.IP, uint32, error) {
		t.Error("lookup after cancellation")
		return nil, 0, nil
	}}
	if _, err := lookupIPContext(ctx, d, "example.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	resolve := MakeResolveLocal(d, &internet.MemoryStreamConfig{})
	if ip, err := resolve(context.Background(), "192.0.2.1"); err != nil || !ip.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("literal IP: %v, %v", ip, err)
	}
}
