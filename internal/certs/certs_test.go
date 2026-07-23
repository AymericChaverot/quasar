package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func selfSignedPEM(t *testing.T, commonName string, sans []string, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "(STAGING) Fake LE Intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     sans,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return base64.StdEncoding.EncodeToString(pemBytes)
}

func TestCollect(t *testing.T) {
	soon := selfSignedPEM(t, "expiring.example.com", []string{"expiring.example.com"}, time.Now().Add(5*24*time.Hour))
	warn := selfSignedPEM(t, "warn.example.com", []string{"warn.example.com", "www.warn.example.com"}, time.Now().Add(20*24*time.Hour))
	fine := selfSignedPEM(t, "fine.example.com", []string{"fine.example.com"}, time.Now().Add(80*24*time.Hour))

	acmeJSON := fmt.Sprintf(`{
		"letsencrypt": {
			"Account": {"Email": "admin@example.com"},
			"Certificates": [
				{"domain": {"main": "fine.example.com", "sans": ["fine.example.com"]}, "certificate": %q, "key": "unused"},
				{"domain": {"main": "warn.example.com", "sans": ["warn.example.com", "www.warn.example.com"]}, "certificate": %q, "key": "unused"},
				{"domain": {"main": "expiring.example.com", "sans": ["expiring.example.com"]}, "certificate": %q, "key": "unused"}
			]
		}
	}`, fine, warn, soon)

	path := filepath.Join(t.TempDir(), "acme.json")
	if err := os.WriteFile(path, []byte(acmeJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d certs, want 3", len(got))
	}

	// Soonest expiry first.
	if got[0].Domain != "expiring.example.com" || got[0].Status != "critical" {
		t.Errorf("got[0] = %+v, want expiring.example.com/critical", got[0])
	}
	if got[1].Domain != "warn.example.com" || got[1].Status != "warning" {
		t.Errorf("got[1] = %+v, want warn.example.com/warning", got[1])
	}
	if got[2].Domain != "fine.example.com" || got[2].Status != "ok" {
		t.Errorf("got[2] = %+v, want fine.example.com/ok", got[2])
	}
	if len(got[1].SANs) != 2 || got[1].SANs[1] != "www.warn.example.com" {
		t.Errorf("got[1].SANs = %v, want 2 entries incl. www.warn.example.com", got[1].SANs)
	}
	if got[0].Issuer != "(STAGING) Fake LE Intermediate" {
		t.Errorf("got[0].Issuer = %q", got[0].Issuer)
	}
}

func TestCollectMissingFile(t *testing.T) {
	if _, err := Collect(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("want error for missing acme.json")
	}
}
