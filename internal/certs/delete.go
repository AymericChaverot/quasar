package certs

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Delete removes a domain's certificate from Traefik's ACME store.
//
// Every other part of the file is carried over as raw JSON rather than
// re-serialised from a model of it: the ACME account and its private key live
// in the same file, and rewriting those from the handful of fields this
// package understands would drop the Let's Encrypt registration.
//
// Traefik reads this file once at startup and keeps the certificates in
// memory, so it has to be restarted afterwards — otherwise the next time it
// saves the store it writes the deleted entry straight back.
func Delete(acmeJSONPath, domain string) error {
	data, err := os.ReadFile(acmeJSONPath)
	if err != nil {
		return err
	}
	var store map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("parse ACME store: %w", err)
	}

	removed := 0
	for _, resolver := range store {
		raw, ok := resolver["Certificates"]
		if !ok {
			continue
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue
		}
		kept := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			var probe struct {
				Domain struct {
					Main string `json:"main"`
				} `json:"domain"`
			}
			if err := json.Unmarshal(entry, &probe); err == nil && strings.EqualFold(probe.Domain.Main, domain) {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == len(entries) {
			continue
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		resolver["Certificates"] = encoded
	}
	if removed == 0 {
		return fmt.Errorf("no certificate for %s in the ACME store", domain)
	}

	out, err := json.Marshal(store)
	if err != nil {
		return err
	}
	// Written beside the store and renamed over it, so a failure half-way
	// through cannot leave Traefik with a truncated file holding its account
	// key. 0600 because the file contains every private key.
	tmp := acmeJSONPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, acmeJSONPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
