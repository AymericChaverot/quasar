package station

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

// The names these tests are written against. The certificate httptest signs
// with covers *.example.com, and nothing resolves them for real — which is
// what makes them the right thing to test an allowlist with. A name is not an
// address, and the whole policy turns on that.
const (
	listedHost   = "api.example.com"
	unlistedHost = "cdn.example.com"
)

// fetcherFor builds a way out with the address check relaxed and the two names
// above pointed at whichever servers the test started. The class rule the
// address check enforces is tested on its own further down, without a socket.
func fetcherFor(t *testing.T, perms Permissions, internal map[string]string, servers map[string]*httptest.Server) *Fetcher {
	t.Helper()
	f := NewFetcher(perms, internal)
	f.allowAddress = nil
	f.dialTo = map[string]string{}
	pool := x509.NewCertPool()
	for host, s := range servers {
		u, err := url.Parse(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		f.dialTo[host] = u.Host
		pool.AddCert(s.Certificate())
	}
	if len(servers) > 0 {
		f.rootCAs = pool
	}
	return f
}

func TestOnlyListedHostsAreReached(t *testing.T) {
	wanted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer wanted.Close()
	unwanted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a host nobody listed was reached")
	}))
	defer unwanted.Close()

	f := fetcherFor(t, Permissions{NetExternal: NetExternal{Allow: []string{listedHost}}}, nil,
		map[string]*httptest.Server{listedHost: wanted, unlistedHost: unwanted})

	resp, err := f.Do(context.Background(), "GET", "https://"+listedHost+"/v2/project/sodium", nil, "")
	if err != nil {
		t.Fatalf("a listed host was refused: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != `{"ok":true}` {
		t.Errorf("response = %+v", resp)
	}

	_, err = f.Do(context.Background(), "GET", "https://"+unlistedHost+"/mod.jar", nil, "")
	if err == nil {
		t.Fatal("an unlisted host was reached")
	}
	// The refusal names what the document does allow, because the author is
	// usually the person reading it.
	if !strings.Contains(err.Error(), listedHost) {
		t.Errorf("the refusal does not say what is allowed: %v", err)
	}
}

// A permission that names hosts and then follows a redirect anywhere is not a
// permission, it is advice.
func TestARedirectToAnUnlistedHostIsRefused(t *testing.T) {
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect was followed to a host nobody listed")
	}))
	defer elsewhere.Close()
	listed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+unlistedHost+"/mod.jar", http.StatusFound)
	}))
	defer listed.Close()

	f := fetcherFor(t, Permissions{NetExternal: NetExternal{Allow: []string{listedHost}}}, nil,
		map[string]*httptest.Server{listedHost: listed, unlistedHost: elsewhere})

	_, err := f.Do(context.Background(), "GET", "https://"+listedHost+"/download", nil, "")
	if err == nil {
		t.Fatal("a redirect off the list was followed")
	}
	if !strings.Contains(err.Error(), unlistedHost) {
		t.Errorf("the refusal does not name where it was being sent: %v", err)
	}
}

// Plain HTTP to the internet is refused outright rather than upgraded: a
// station that meant to fetch over HTTP meant to fetch something it should not
// be trusting.
func TestPlainHTTPToTheInternetIsRefused(t *testing.T) {
	f := fetcherFor(t, Permissions{NetExternal: NetExternal{Allow: []string{listedHost}}}, nil, nil)

	_, err := f.Do(context.Background(), "GET", "http://"+listedHost+"/v2/project/x", nil, "")
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error is %v, want a refusal naming the scheme", err)
	}
}

func TestABodyOverTheCapIsRefused(t *testing.T) {
	big := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < (MaxHTTPBody>>20)+2; i++ {
			w.Write([]byte(chunk))
		}
	}))
	defer big.Close()

	f := fetcherFor(t, Permissions{NetExternal: NetExternal{Allow: []string{listedHost}}}, nil,
		map[string]*httptest.Server{listedHost: big})

	_, err := f.Do(context.Background(), "GET", "https://"+listedHost+"/huge", nil, "")
	if err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("error is %v, want a refusal naming the cap", err)
	}
}

// quasar.service hands out the address rather than letting a script write one,
// so a station never learns another container's name and has nothing to guess
// with.
func TestServiceURLIsOnlyForDeclaredServicesAndPorts(t *testing.T) {
	perms := Permissions{NetInternal: NetInternal{Services: []string{"minecraft"}, Ports: []int{8123}}}
	f := fetcherFor(t, perms, map[string]string{"minecraft": "qs-abcd1234-minecraft-1"}, nil)

	got, err := f.ServiceURL("minecraft", 8123)
	if err != nil {
		t.Fatalf("a declared service was refused: %v", err)
	}
	if got != "http://qs-abcd1234-minecraft-1:8123" {
		t.Errorf("ServiceURL = %q", got)
	}

	for _, c := range []struct {
		service string
		port    int
	}{
		{"minecraft", 25575}, // a port nobody declared
		{"db", 8123},         // a service nobody declared
	} {
		if _, err := f.ServiceURL(c.service, c.port); err == nil {
			t.Errorf("ServiceURL(%q, %d) was allowed", c.service, c.port)
		}
	}
}

// The two permissions are opposites, and the check that keeps them so is on
// the address a name resolved to rather than on the name. A listed host whose
// DNS answers 127.0.0.1 is otherwise a way into whatever this dashboard can
// reach.
func TestTheAddressClassIsCheckedNotJustTheName(t *testing.T) {
	for _, c := range []struct {
		addr     string
		internal bool
		ok       bool
		why      string
	}{
		{"93.184.216.34", false, true, "a public address for net.external"},
		{"127.0.0.1", false, false, "an allowlisted host resolving to the loopback"},
		{"10.0.0.5", false, false, "or to this server's own network"},
		{"169.254.169.254", false, false, "or to the cloud metadata service"},
		{"100.64.0.1", false, false, "or to carrier-grade NAT, where a neighbour lives"},

		{"172.18.0.4", true, true, "a container address for net.internal"},
		{"127.0.0.1", true, true, "and the loopback, which is still this machine"},
		{"93.184.216.34", true, false, "but net.internal does not reach outwards"},
	} {
		if err := addressOfClass(c.internal, netip.MustParseAddr(c.addr)); (err == nil) != c.ok {
			t.Errorf("%s (%s, internal=%v): err = %v", c.why, c.addr, c.internal, err)
		}
	}
}

// A station granted nothing reaches nothing, which is what every station
// starts as.
func TestNoNetworkPermissionReachesNothing(t *testing.T) {
	f := fetcherFor(t, Permissions{}, nil, nil)

	for _, raw := range []string{"https://example.com", "http://localhost:8080", "https://127.0.0.1"} {
		if _, err := f.Do(context.Background(), "GET", raw, nil, ""); err == nil {
			t.Errorf("%s was reached by a station granted no network at all", raw)
		}
	}
	if _, err := f.ServiceURL("anything", 80); err == nil {
		t.Error("an internal address was handed out to a station granted none")
	}
}

// A container name that does not resolve is the one failure here that is
// nobody's mistake: in local development the dashboard runs on the host rather
// than as a container on the Traefik network, so it cannot see the names it
// correctly handed out. Saying "no such host" leaves an operator looking for a
// bug that is not there.
func TestAnUnresolvedContainerSaysWhyRatherThanNoSuchHost(t *testing.T) {
	perms := Permissions{NetInternal: NetInternal{Services: []string{"web"}, Ports: []int{80}}}
	f := NewFetcher(perms, map[string]string{"web": "qs-abcd1234-web-1"})

	_, err := f.Do(context.Background(), "GET", "http://qs-abcd1234-web-1:80/", nil, "")
	if err == nil {
		t.Fatal("a container name that does not exist resolved")
	}
	if !strings.Contains(err.Error(), "Traefik network") {
		t.Errorf("the message does not explain what happened: %v", err)
	}

	// A host that answers badly is still reported as itself.
	if got := UnresolvedInternal("x", errors.New("connection refused")); got.Error() != "connection refused" {
		t.Errorf("an ordinary failure was rewritten: %v", got)
	}
}
