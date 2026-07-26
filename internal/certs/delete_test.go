package certs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeStore(t *testing.T) string {
	t.Helper()
	a := selfSignedPEM(t, "keep.example.com", []string{"keep.example.com"}, time.Now().Add(60*24*time.Hour))
	b := selfSignedPEM(t, "gone.example.com", []string{"gone.example.com"}, time.Now().Add(60*24*time.Hour))
	store := fmt.Sprintf(`{
		"letsencrypt": {
			"Account": {"Email": "admin@example.com", "PrivateKey": "the-account-key", "KeyType": "4096"},
			"Certificates": [
				{"domain": {"main": "keep.example.com", "sans": ["keep.example.com"]}, "certificate": %q, "key": "unused"},
				{"domain": {"main": "gone.example.com", "sans": ["gone.example.com"]}, "certificate": %q, "key": "unused"}
			]
		}
	}`, a, b)

	path := filepath.Join(t.TempDir(), "acme.json")
	if err := os.WriteFile(path, []byte(store), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDelete(t *testing.T) {
	path := writeStore(t)
	if err := Delete(path, "gone.example.com"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect after Delete: %v", err)
	}
	if len(got) != 1 || got[0].Domain != "keep.example.com" {
		t.Fatalf("remaining certs = %+v, want only keep.example.com", got)
	}

	// The account and its key must survive untouched: losing them would force a
	// re-registration with Let's Encrypt.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var store map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatalf("rewritten store is not valid JSON: %v", err)
	}
	var account struct {
		Email      string
		PrivateKey string
		KeyType    string
	}
	if err := json.Unmarshal(store["letsencrypt"]["Account"], &account); err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.PrivateKey != "the-account-key" || account.Email != "admin@example.com" || account.KeyType != "4096" {
		t.Errorf("account = %+v, want it preserved verbatim", account)
	}
}

// Deleting the last certificate must leave an empty list, not a missing key or
// a null Traefik would refuse to load.
func TestDeleteEveryCertificate(t *testing.T) {
	path := writeStore(t)
	for _, domain := range []string{"keep.example.com", "gone.example.com"} {
		if err := Delete(path, domain); err != nil {
			t.Fatalf("Delete %s: %v", domain, err)
		}
	}
	got, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d certs, want none", len(got))
	}
	data, _ := os.ReadFile(path)
	var store map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatal(err)
	}
	if string(store["letsencrypt"]["Certificates"]) != "[]" {
		t.Errorf("Certificates = %s, want []", store["letsencrypt"]["Certificates"])
	}
}

func TestDeleteUnknownDomain(t *testing.T) {
	path := writeStore(t)
	if err := Delete(path, "absent.example.com"); err == nil {
		t.Fatal("want an error for a domain the store does not hold")
	}
	// The store must be left alone when nothing matched.
	if got, _ := Collect(path); len(got) != 2 {
		t.Errorf("got %d certs, want both left in place", len(got))
	}
}
