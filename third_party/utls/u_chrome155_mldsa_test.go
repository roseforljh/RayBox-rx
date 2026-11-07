package tls

import (
	"context"
	"crypto"
	"crypto/mldsa"
	"crypto/rand"
	stdtls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestChrome155MLDSAHandshake(t *testing.T) {
	for _, params := range []mldsa.Parameters{mldsa.MLDSA44(), mldsa.MLDSA65(), mldsa.MLDSA87()} {
		t.Run(params.String(), func(t *testing.T) {
			key, err := mldsa.GenerateKey(params)
			if err != nil {
				t.Fatal(err)
			}
			template := &x509.Certificate{
				SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"},
				DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Hour),
				NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
				ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				BasicConstraintsValid: true,
			}
			der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
			if err != nil {
				t.Fatal(err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(cert)
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := UClient(a, &Config{ServerName: "example.com", RootCAs: roots}, HelloChrome_155)
			server := stdtls.Server(b, &stdtls.Config{
				MinVersion: stdtls.VersionTLS13, MaxVersion: stdtls.VersionTLS13,
				SessionTicketsDisabled: true,
				Certificates:           []stdtls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
			})
			result := make(chan error, 1)
			go func() {
				if err := server.HandshakeContext(ctx); err != nil {
					result <- err
					return
				}
				var payload [4]byte
				if _, err := io.ReadFull(server, payload[:]); err != nil {
					result <- err
					return
				}
				_, err := server.Write(payload[:])
				result <- err
			}()
			if err := client.HandshakeContext(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write([]byte("echo")); err != nil {
				t.Fatal(err)
			}
			var reply [4]byte
			if _, err := io.ReadFull(client, reply[:]); err != nil {
				t.Fatal(err)
			}
			if string(reply[:]) != "echo" {
				t.Fatal("payload changed")
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			state := client.ConnectionState()
			if !state.HandshakeComplete || state.DidResume || len(state.VerifiedChains) == 0 {
				t.Fatal("expected a verified fresh TLS 1.3 handshake")
			}
			pub, ok := state.PeerCertificates[0].PublicKey.(*mldsa.PublicKey)
			if !ok || pub.Parameters() != params {
				t.Fatal("wrong ML-DSA server key")
			}
		})
	}
}

func TestChrome155MLDSASignatureValidation(t *testing.T) {
	for _, params := range []mldsa.Parameters{mldsa.MLDSA44(), mldsa.MLDSA65(), mldsa.MLDSA87()} {
		t.Run(params.String(), func(t *testing.T) {
			key, err := mldsa.GenerateKey(params)
			if err != nil {
				t.Fatal(err)
			}
			message := []byte("TLS 1.3, server CertificateVerify")
			sig, err := key.Sign(rand.Reader, message, crypto.Hash(0))
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyHandshakeSignature(signatureMLDSA, key.Public(), directSigning, message, sig); err != nil {
				t.Fatal(err)
			}
			sig[0] ^= 1
			if err := verifyHandshakeSignature(signatureMLDSA, key.Public(), directSigning, message, sig); err == nil {
				t.Fatal("accepted a corrupted ML-DSA signature")
			}
		})
	}
}
