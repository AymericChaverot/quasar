// Package certs reads the TLS certificates Traefik has obtained via ACME, so
// the dashboard can surface their expiry without talking to Traefik itself —
// it only has API access through the restricted socket-proxy, which doesn't
// expose certificate resolver state.
package certs

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"sort"
	"time"
)

// Cert is one certificate Traefik holds, with the fields the dashboard shows.
type Cert struct {
	Domain   string
	SANs     []string
	Issuer   string
	NotAfter time.Time
	DaysLeft int
	Status   string // "ok", "warning" (<30d), "critical" (<7d or expired)
}

// acmeStore mirrors the shape of Traefik's acme.json: a map of resolver name
// (e.g. "letsencrypt") to its account and certificate list.
type acmeStore map[string]struct {
	Certificates []struct {
		Domain struct {
			Main string   `json:"main"`
			SANs []string `json:"sans"`
		} `json:"domain"`
		Certificate string `json:"certificate"`
	} `json:"Certificates"`
}

// Collect reads and parses Traefik's ACME storage file, returning the
// certificates it holds sorted by soonest expiry first.
func Collect(acmeJSONPath string) ([]Cert, error) {
	data, err := os.ReadFile(acmeJSONPath)
	if err != nil {
		return nil, err
	}
	var store acmeStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}

	var out []Cert
	now := time.Now()
	for _, resolver := range store {
		for _, entry := range resolver.Certificates {
			pemBytes, err := base64.StdEncoding.DecodeString(entry.Certificate)
			if err != nil {
				continue
			}
			block, _ := pem.Decode(pemBytes)
			if block == nil {
				continue
			}
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				continue
			}

			domain := entry.Domain.Main
			if domain == "" {
				domain = leaf.Subject.CommonName
			}
			daysLeft := int(leaf.NotAfter.Sub(now).Hours() / 24)
			status := "ok"
			switch {
			case leaf.NotAfter.Before(now), daysLeft < 7:
				status = "critical"
			case daysLeft < 30:
				status = "warning"
			}

			out = append(out, Cert{
				Domain:   domain,
				SANs:     entry.Domain.SANs,
				Issuer:   leaf.Issuer.CommonName,
				NotAfter: leaf.NotAfter,
				DaysLeft: daysLeft,
				Status:   status,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NotAfter.Before(out[j].NotAfter) })
	return out, nil
}
