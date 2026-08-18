package station

// The only way out of a station, and the narrowest thing in this package.
//
// Two permissions meet here and they are opposites. net.external reaches named
// hosts on the public internet and nothing else — no wildcards, no plain HTTP,
// and a redirect is followed only to a host also on the list, because otherwise
// the list is advice. net.internal reaches the application's own containers on
// this server and must never reach outwards, which is the same rule read from
// the other side.
//
// Both are checked twice: once on the URL, which is what an author reads in the
// refusal, and once on the address the name actually resolved to, which is what
// stops a listed host whose DNS answers 127.0.0.1 from becoming a way into the
// dashboard. A name is not an address until something resolves it, and the
// resolution is where an allowlist is usually defeated.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// MaxHTTPBody caps one response. A station fetches a mod or an API answer; a
// body approaching this is not something to be moving through a script.
const MaxHTTPBody = 8 << 20

// httpTimeout bounds one request, well inside the budget of the call around it
// so that a slow host comes back as a failed action rather than as a worker
// killed from outside.
const httpTimeout = 30 * time.Second

// Response is what a station gets back.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Fetcher is one station's way out, for one call.
type Fetcher struct {
	perms Permissions

	// internal maps a service the document named to the host the parent
	// resolved for it. A URL is internal because it is one of these, not
	// because it looks like one.
	internal map[string]string

	// allowAddress decides whether an address a name resolved to is of the
	// right class. Replaced in tests, where every server is on the loopback
	// and the check would otherwise refuse all of them.
	allowAddress func(internal bool, addr netip.Addr) error

	// rootCAs are the certificate authorities to trust, nil for the system's
	// own — which is what a station reaching the public internet wants. Tests
	// set it to the authority their own server signed itself with.
	rootCAs *x509.CertPool

	// dialTo sends a hostname to a particular address instead of resolving it.
	// Only tests set it: an allowlist is a list of names, and the only way to
	// test what the names do is with names that resolve nowhere real.
	dialTo map[string]string
}

// NewFetcher builds the way out for one station. internal maps each service
// the net.internal permission names to the host it can be reached at on this
// server; it is empty for a station that was granted no internal access.
func NewFetcher(perms Permissions, internal map[string]string) *Fetcher {
	return &Fetcher{perms: perms, internal: internal, allowAddress: addressOfClass}
}

// ServiceURL is what quasar.service(name, port) returns: a base URL on the
// internal network, for a service and a port the document declared.
//
// Handing out the address rather than letting a script write one is what keeps
// the permission meaningful — a station never learns another container's name
// and has nothing to guess with.
func (f *Fetcher) ServiceURL(service string, port int) (string, error) {
	if !f.perms.AllowsInternal(service, port) {
		return "", fmt.Errorf("reaching %s on port %d: this station's net.internal permission does not cover it",
			service, port)
	}
	host, ok := f.internal[service]
	if !ok || host == "" {
		return "", fmt.Errorf("%s has no container running on this server", service)
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// Do performs one request, after deciding it is allowed to.
func (f *Fetcher) Do(ctx context.Context, method, raw string, headers map[string]string, body string) (Response, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Response{}, fmt.Errorf("%q is not a URL", raw)
	}
	internal, err := f.allowURL(u)
	if err != nil {
		return Response{}, err
	}

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return Response{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Quasar-Station/1")
	}

	resp, err := f.client(internal).Do(req)
	if err != nil {
		return Response{}, readableFetchError(err, u.Hostname(), internal)
	}
	defer resp.Body.Close()

	// One byte past the cap, which is the only way to tell a body that exactly
	// fills it from one that overran.
	read, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPBody+1))
	if err != nil {
		return Response{}, fmt.Errorf("reading the answer from %s: %w", u.Host, err)
	}
	if len(read) > MaxHTTPBody {
		return Response{}, fmt.Errorf("%s answered with more than the %d MB a station may read",
			u.Host, MaxHTTPBody>>20)
	}

	out := Response{Status: resp.StatusCode, Headers: map[string]string{}, Body: string(read)}
	for k := range resp.Header {
		out.Headers[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return out, nil
}

// allowURL is the policy, in the words an author needs. It reports whether the
// URL is an internal one, which decides which client performs it.
func (f *Fetcher) allowURL(u *url.URL) (bool, error) {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false, fmt.Errorf("%q names no host", u.String())
	}

	// An internal URL is one this station was handed. Plain HTTP is right
	// here: the hop never leaves the machine, and the containers on the other
	// end do not have certificates.
	for service, target := range f.internal {
		if host != strings.ToLower(target) {
			continue
		}
		port, _ := strconv.Atoi(u.Port())
		if !f.perms.AllowsInternal(service, port) {
			return false, fmt.Errorf("reaching %s on port %d: this station's net.internal permission does not cover it",
				service, port)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return false, fmt.Errorf("%s is not a scheme a station can use", u.Scheme)
		}
		return true, nil
	}

	if u.Scheme != "https" {
		return false, fmt.Errorf("%s://%s: a station reaches the internet over HTTPS only", u.Scheme, host)
	}
	if !f.perms.AllowsHost(host) {
		return false, fmt.Errorf("reaching %s: this station's net.external permission names %s and nothing else",
			host, allowedList(f.perms.NetExternal.Allow))
	}
	return false, nil
}

func allowedList(hosts []string) string {
	if len(hosts) == 0 {
		return "no host at all"
	}
	return strings.Join(hosts, ", ")
}

// client builds the one that performs a request of this class.
//
// Two rather than one, and the difference is the dialler: the address a name
// resolved to has to be of the right kind, and "the right kind" is opposite
// for the two permissions. Redirects are re-checked against the same policy,
// so a listed host cannot hand out a hop to an unlisted one.
func (f *Fetcher) client(internal bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	dialer.Control = func(_, address string, _ syscall.RawConn) error {
		return f.checkAddress(internal, address)
	}
	dial := dialer.DialContext
	if len(f.dialTo) > 0 {
		base := dial
		dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			if host, _, err := net.SplitHostPort(address); err == nil {
				if to, ok := f.dialTo[host]; ok {
					address = to
				}
			}
			return base(ctx, network, address)
		}
	}
	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			DialContext:       dial,
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: f.rootCAs},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			next, err := f.allowURL(req.URL)
			if err != nil {
				return err
			}
			if next != internal {
				return fmt.Errorf("%s redirects across this station's network permissions", req.URL.Host)
			}
			return nil
		},
	}
}

// checkAddress is the second half of the policy, run on what the name actually
// resolved to. Without it, an allowlisted host whose DNS answers 127.0.0.1 is
// a way into whatever this dashboard can reach.
func (f *Fetcher) checkAddress(internal bool, address string) error {
	if f.allowAddress == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%s is not an address", host)
	}
	return f.allowAddress(internal, addr.Unmap())
}

// addressOfClass refuses an address on the wrong side of the machine.
func addressOfClass(internal bool, addr netip.Addr) error {
	if isLocal(addr) == internal {
		return nil
	}
	if internal {
		return fmt.Errorf("%s is not on this server; net.internal does not reach outwards", addr)
	}
	return fmt.Errorf("%s is on this server's own network; net.external does not reach inwards", addr)
}

// isLocal reports an address that is not on the public internet.
func isLocal(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() || isCGNAT(addr)
}

// cgnat is the carrier-grade NAT range, which is neither public nor private by
// the usual definitions and is exactly where a cloud metadata service or a
// neighbouring tenant tends to live.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isCGNAT(addr netip.Addr) bool { return addr.Is4() && cgnat.Contains(addr) }

// readableFetchError keeps the policy's own refusals readable when they come
// back wrapped in url.Error from the redirect check or the dialler, and says
// something useful about the one failure that is nobody's mistake.
func readableFetchError(err error, host string, internal bool) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	if internal && isUnresolved(err) {
		return fmt.Errorf("%s did not resolve. The dashboard reaches an application's own "+
			"containers over the Traefik network, which it is only attached to when it runs as a "+
			"container itself — as it does on a server, but not in local development", host)
	}
	return err
}

// isUnresolved reports a name the resolver could not answer for, as opposed to
// a host that answered badly.
func isUnresolved(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	return strings.Contains(err.Error(), "no such host")
}

// UnresolvedInternal is that same explanation, for the places that reach a
// container without going through Do — the embedded page's proxy.
func UnresolvedInternal(host string, err error) error {
	if !isUnresolved(err) {
		return err
	}
	return readableFetchError(err, host, true)
}

// InternalServices lists the services a station's net.internal permission
// names, so the parent knows which containers to resolve before a call.
func (p Permissions) InternalServices() []string { return slices.Clone(p.NetInternal.Services) }
