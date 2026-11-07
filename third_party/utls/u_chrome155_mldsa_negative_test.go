package tls

import (
	"crypto/mldsa"
	"testing"
)

func TestMLDSASignatureSchemeMatchesParameters(t *testing.T) {
	params := []mldsa.Parameters{mldsa.MLDSA44(), mldsa.MLDSA65(), mldsa.MLDSA87()}
	schemes := []SignatureScheme{MLDSA44, MLDSA65, MLDSA87}
	for i, keyParams := range params {
		key, err := mldsa.GenerateKey(keyParams)
		if err != nil {
			t.Fatal(err)
		}
		for j, scheme := range schemes {
			want := i == j
			if got := signatureAlgorithmMatchesPublicKey(VersionTLS13, scheme, key.Public()); got != want {
				t.Errorf("%s with scheme %v: got %v, want %v", keyParams, scheme, got, want)
			}
		}
		if signatureAlgorithmMatchesPublicKey(VersionTLS12, MLDSA44, key.Public()) {
			t.Errorf("%s accepted ML-DSA in TLS 1.2", keyParams)
		}
	}
}

func TestMLDSASelectionRejectsTLS12(t *testing.T) {
	key, err := mldsa.GenerateKey(mldsa.MLDSA44())
	if err != nil {
		t.Fatal(err)
	}
	cert := &Certificate{PrivateKey: key}
	if got := signatureSchemesForCertificate(VersionTLS12, cert); got != nil {
		t.Fatalf("TLS 1.2 advertised ML-DSA schemes: %v", got)
	}
	if _, err := selectSignatureScheme(VersionTLS12, cert, []SignatureScheme{MLDSA44}); err == nil {
		t.Fatal("TLS 1.2 selected ML-DSA")
	}
}
