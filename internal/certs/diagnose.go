package certs

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// HostCheck answers, for one hostname an app is routed on, the two questions an
// operator has when a site is served without TLS: does Traefik hold a
// certificate for it, and does the name point at this server at all.
type HostCheck struct {
	Host     string
	HasCert  bool
	DaysLeft int
	IPs      []string // what DNS returns for the host, empty when it does not resolve
	Problem  string   // human explanation, empty when nothing is wrong
}

// lookupTimeout bounds the DNS work a page render waits on.
const lookupTimeout = 3 * time.Second

// Diagnose pairs each hostname with the certificate Traefik holds for it and,
// when one is missing, looks up why.
//
// Certificates are issued over the HTTP-01 challenge, so a name that does not
// resolve to this server can never be validated — and that failure is only
// visible in Traefik's logs, which is why an app can sit there deployed and
// permanently without TLS. The apex is the case that bites: a wildcard
// *.example.com record covers admin.example.com and every app subdomain, but
// not example.com itself, so an app on "@" needs its own A record.
//
// The reference for "this server" is the dashboard's own hostname: it resolves
// correctly by definition, since its page is what asks the question.
func Diagnose(ctx context.Context, held []Cert, rootDomain string, hosts []string) []HostCheck {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	// localhost is the development default, where no DNS answer means anything.
	checkDNS := rootDomain != "" && rootDomain != "localhost"
	var serverIPs []string
	if checkDNS {
		serverIPs = lookup(ctx, "admin."+rootDomain)
	}

	out := make([]HostCheck, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		out[i] = HostCheck{Host: host}
		for _, c := range held {
			if covers(c, host) {
				out[i].HasCert = true
				out[i].DaysLeft = c.DaysLeft
				break
			}
		}
		// A host that already has a certificate resolved correctly at issuance;
		// only the ones without need explaining.
		if out[i].HasCert || !checkDNS {
			continue
		}
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			out[i].IPs = lookup(ctx, host)
			out[i].Problem = explain(host, rootDomain, out[i].IPs, serverIPs)
		}(i, host)
	}
	wg.Wait()
	return out
}

// explain turns a failed lookup into the action that fixes it.
func explain(host, rootDomain string, hostIPs, serverIPs []string) string {
	if len(hostIPs) == 0 {
		msg := host + " does not resolve, so Let's Encrypt cannot validate it."
		if host == rootDomain {
			msg += " A wildcard *." + rootDomain + " record does not cover the root domain itself — add an A record for " + rootDomain + " pointing at this server."
		} else {
			msg += " Point an A record at this server."
		}
		return msg
	}
	if len(serverIPs) > 0 && !overlap(hostIPs, serverIPs) {
		return host + " resolves to " + strings.Join(hostIPs, ", ") +
			", but this server answers on " + strings.Join(serverIPs, ", ") +
			". Let's Encrypt validates against the address DNS returns."
	}
	return "DNS is correct but no certificate has been issued yet. Traefik requests one right after a deploy; if it does not appear within a minute, its logs carry the ACME error."
}

// covers reports whether a certificate's main domain or one of its SANs
// matches the host, wildcards included.
func covers(c Cert, host string) bool {
	if matchDomain(c.Domain, host) {
		return true
	}
	for _, san := range c.SANs {
		if matchDomain(san, host) {
			return true
		}
	}
	return false
}

// matchDomain compares a certificate name to a host. A wildcard covers exactly
// one label: *.example.com matches app.example.com but neither example.com nor
// a.b.example.com.
func matchDomain(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	if pattern == host {
		return true
	}
	rest, ok := strings.CutPrefix(pattern, "*.")
	if !ok {
		return false
	}
	label, parent, found := strings.Cut(host, ".")
	return found && label != "" && parent == rest
}

// lookup resolves a hostname, returning no addresses rather than an error:
// every caller here treats "did not resolve" the same way.
func lookup(ctx context.Context, host string) []string {
	var r net.Resolver
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	sort.Strings(addrs)
	return addrs
}

func overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
