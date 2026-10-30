package tls

import (
	"bytes"
	gotls "crypto/tls"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/protocol/tls/cert"
)

func TestCertificateReloadPublishesImmutableSnapshots(t *testing.T) {
	first, _ := cert.MustGenerate(nil, cert.CommonName("before.example"), cert.DNSNames("before.example"))
	second, _ := cert.MustGenerate(nil, cert.CommonName("after.example"), cert.DNSNames("after.example"))
	entry := ParseCertificate(first)
	secondPEM, secondKey := second.ToPEM()
	entry.CertificatePath = filepath.Join(t.TempDir(), "cert.pem")
	entry.KeyPath = filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(entry.CertificatePath, secondPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.KeyPath, secondKey, 0600); err != nil {
		t.Fatal(err)
	}
	config := &Config{Certificate: []*Certificate{entry}}
	initialPEM := append([]byte(nil), entry.Certificate...)
	stable := config.BuildCertificates()
	if len(stable) != 1 || stable[0].Leaf.Subject.CommonName != "before.example" {
		t.Fatal("invalid initial snapshot")
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			// Construct selectors concurrently from one protobuf configuration.
			// The immediate file reload must not mutate shared PEM inputs.
			selector := config.GetTLSConfig().GetCertificate
			deadline := time.Now().Add(3 * time.Second)
			for {
				current, err := selector(&gotls.ClientHelloInfo{ServerName: "after.example"})
				if err != nil {
					t.Error(err)
					return
				}
				name := current.Leaf.Subject.CommonName
				if name != "before.example" && name != "after.example" {
					t.Errorf("mixed certificate: %q", name)
					return
				}
				if name == "after.example" {
					break
				}
				if time.Now().After(deadline) {
					t.Error("hot reload failed to become visible")
					return
				}
				time.Sleep(time.Millisecond)
			}
			for range 1000 {
				current, err := selector(&gotls.ClientHelloInfo{ServerName: "after.example"})
				if err != nil || current.Leaf.Subject.CommonName != "after.example" {
					t.Error("unstable reloaded certificate", err)
					return
				}
				if stable[0].Leaf.Subject.CommonName != "before.example" {
					t.Error("published old snapshot mutated")
					return
				}
			}
		})
	}
	wg.Wait()
	if !bytes.Equal(initialPEM, entry.Certificate) {
		t.Fatal("reloader modified caller's protobuf certificate")
	}
	if stable[0].Leaf.Subject.CommonName != "before.example" {
		t.Fatal("BuildCertificates snapshot was asynchronously replaced")
	}
}

func TestReloadedCertificateSelectorRetainsSNIPolicy(t *testing.T) {
	certificate, _ := cert.MustGenerate(nil, cert.CommonName("service.example"), cert.DNSNames("*.example"))
	entry := ParseCertificate(certificate)
	entry.OneTimeLoading = true
	c := &Config{Certificate: []*Certificate{entry}}
	selectCertificate := getNewGetCertificateFunc(c.buildCertificates(true), true)
	for _, name := range []string{"service.example", "other.example"} {
		if _, err := selectCertificate(&gotls.ClientHelloInfo{ServerName: name}); err != nil {
			t.Errorf("allowed name %q: %v", name, err)
		}
	}
	if _, err := selectCertificate(&gotls.ClientHelloInfo{ServerName: "unrelated.test"}); err == nil {
		t.Fatal("unknown SNI accepted")
	}
}
