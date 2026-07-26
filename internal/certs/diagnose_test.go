package certs

import (
	"context"
	"strings"
	"testing"
)

func TestMatchDomain(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"example.com", "example.com", true},
		{"Example.COM", "example.com", true},
		{"example.com", "app.example.com", false},
		{"*.example.com", "app.example.com", true},
		// A wildcard covers one label: neither the apex nor a deeper name.
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", ".example.com", false},
	}
	for _, c := range cases {
		if got := matchDomain(c.pattern, c.host); got != c.want {
			t.Errorf("matchDomain(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
		}
	}
}

// The dev domain switches DNS off, so this exercises the certificate matching
// on its own without reaching a resolver.
func TestDiagnoseMatchesHeldCertificates(t *testing.T) {
	held := []Cert{
		{Domain: "example.com", SANs: []string{"example.com"}, DaysLeft: 60},
		{Domain: "blog.example.com", SANs: []string{"blog.example.com", "www.myblog.fr"}, DaysLeft: 12},
	}
	got := Diagnose(context.Background(), held, "localhost",
		[]string{"example.com", "www.myblog.fr", "shop.example.com"})

	if len(got) != 3 {
		t.Fatalf("got %d checks, want 3", len(got))
	}
	if !got[0].HasCert || got[0].DaysLeft != 60 {
		t.Errorf("got[0] = %+v, want a cert with 60 days left", got[0])
	}
	// Matched through a SAN rather than the main domain.
	if !got[1].HasCert || got[1].DaysLeft != 12 {
		t.Errorf("got[1] = %+v, want the blog cert via its SAN", got[1])
	}
	if got[2].HasCert {
		t.Errorf("got[2] = %+v, want no certificate", got[2])
	}
}

// The apex is the case worth spelling out: a wildcard record covers every app
// subdomain but not the root domain, which leaves an app on "@" without TLS.
func TestExplainApexNeedsItsOwnRecord(t *testing.T) {
	msg := explain("example.com", "example.com", nil, []string{"203.0.113.7"})
	if !strings.Contains(msg, "*.example.com") || !strings.Contains(msg, "A record for example.com") {
		t.Errorf("explain() = %q, want the wildcard-does-not-cover-the-apex advice", msg)
	}

	sub := explain("blog.example.com", "example.com", nil, []string{"203.0.113.7"})
	if strings.Contains(sub, "*.example.com") {
		t.Errorf("explain() = %q, want no apex advice for a subdomain", sub)
	}
}

func TestExplainWrongAddress(t *testing.T) {
	msg := explain("blog.example.com", "example.com", []string{"198.51.100.9"}, []string{"203.0.113.7"})
	if !strings.Contains(msg, "198.51.100.9") || !strings.Contains(msg, "203.0.113.7") {
		t.Errorf("explain() = %q, want both the resolved and the expected address", msg)
	}

	// Same address: DNS is fine, so the answer must not blame it.
	ok := explain("blog.example.com", "example.com", []string{"203.0.113.7"}, []string{"203.0.113.7"})
	if strings.Contains(ok, "resolves to") {
		t.Errorf("explain() = %q, want no DNS complaint when the address matches", ok)
	}
}
