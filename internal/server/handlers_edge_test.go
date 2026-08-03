package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// edgeServer is a server with a keyring, which is all the edge-auth handlers
// need beyond the app they are given: no database, no Docker.
func edgeServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	k, err := secrets.KeyringFrom(key)
	if err != nil {
		t.Fatal(err)
	}
	s.keyring = k
	s.edgeAttempts = newEdgeThrottle()
	return s
}

// edgeApp is an app protected by "ops" / "s3cret".
func edgeApp(t *testing.T) *db.App {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return &db.App{
		ID: "beef0001", Name: "API", Subdomain: "api",
		BasicAuthUser: "ops", BasicAuthHash: string(hash),
	}
}

// edgeRequest is Traefik asking whether a visitor may have a page — the shape
// the login page is actually meant for.
func edgeRequest(target string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/edge-auth/beef0001", nil)
	r.Header.Set("X-Forwarded-Uri", target)
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.Header.Set("Sec-Fetch-Dest", "document")
	return r
}

// A visitor with no session gets the page, not the browser's own credentials
// box. The absent WWW-Authenticate header is what decides that: with it, every
// browser draws its box over whatever the body says.
func TestEdgeLoginPageReplacesTheBrowserPrompt(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	w := httptest.NewRecorder()
	s.edgeLoginPage(w, edgeRequest("/dashboard"), a, "/dashboard", false)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q — the browser will draw its own box over the page", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<form") || !strings.Contains(body, a.Name) {
		t.Errorf("the response is not the login page:\n%s", body)
	}
	// Served on the app's own domain, where any "/static/..." would be asked of
	// the app rather than of Quasar, and any CDN would have to be reachable.
	if strings.Contains(body, "/static/") || strings.Contains(body, "//unpkg") || strings.Contains(body, "cdn.") {
		t.Errorf("the page loads something it cannot be sure to reach:\n%s", body)
	}
	// The password field must never be prefilled from the request.
	if !strings.Contains(body, `id="p"`) || strings.Contains(body, "s3cret") {
		t.Errorf("unexpected password field:\n%s", body)
	}
}

// The whole point of the middleware: without credentials the request must not
// reach the app, and with them it must.
func TestEdgeAuthAuthorisesOnlyWithTheAppsCredentials(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)

	tests := []struct {
		name       string
		user, pass string
		want       int
	}{
		{"right credentials", "ops", "s3cret", http.StatusOK},
		{"wrong password", "ops", "hunter2", http.StatusUnauthorized},
		{"wrong username", "root", "s3cret", http.StatusUnauthorized},
		{"empty password", "ops", "", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s.edgeAttempts = newEdgeThrottle()
			r := edgeRequest("/")
			r.SetBasicAuth(tc.user, tc.pass)
			w := httptest.NewRecorder()
			s.handleEdgeAuthApp(w, r, a)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// Signing in has to answer outside 2xx: a 2xx tells Traefik to forward the
// request to the app instead of returning our response, which would drop the
// Set-Cookie the whole exchange exists to deliver.
func TestEdgeSignInReturnsTheCookieOutsideTwoXX(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	r := edgeRequest(edgeAuthPath + "?next=%2Freports")
	r.SetBasicAuth("ops", "s3cret")
	w := httptest.NewRecorder()
	s.handleEdgeAuthApp(w, r, a)

	if w.Code < 300 || w.Code > 399 {
		t.Fatalf("status = %d, want a redirect; a 2xx would send this to the app", w.Code)
	}
	if got := w.Header().Get("Location"); !strings.HasSuffix(got, "/reports") {
		t.Errorf("Location = %q, want the page the visitor asked for", got)
	}
	cookie := w.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != edgeCookiePrefix+a.ID {
		t.Fatalf("cookies = %+v, want one session cookie for this app", cookie)
	}
	c := cookie[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie is not HttpOnly+Secure+Lax: %+v", c)
	}
	if c.Domain != "" {
		t.Errorf("cookie Domain = %q, want it host-only so siblings do not receive it", c.Domain)
	}
	if strings.Contains(c.Value, a.BasicAuthUser) || strings.Contains(c.Value, a.BasicAuthHash) {
		t.Errorf("the cookie carries the credentials: %q", c.Value)
	}
}

// A wrong password at the sign-in step comes back as the page saying so, and
// still without a cookie.
func TestEdgeSignInRefusesWrongCredentials(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	r := edgeRequest(edgeAuthPath + "?next=%2F")
	r.SetBasicAuth("ops", "hunter2")
	w := httptest.NewRecorder()
	s.handleEdgeAuthApp(w, r, a)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Errorf("a refused sign-in still set a cookie: %+v", w.Result().Cookies())
	}
	if !strings.Contains(w.Body.String(), "Wrong username or password") {
		t.Errorf("the page does not say the credentials were refused:\n%s", w.Body.String())
	}
}

// A session cookie this dashboard issued lets the visitor through without the
// password being sent again.
func TestEdgeSessionCookieAuthorises(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	token, err := s.edgeToken(a)
	if err != nil {
		t.Fatal(err)
	}
	r := edgeRequest("/")
	r.AddCookie(&http.Cookie{Name: edgeCookiePrefix + a.ID, Value: token})
	w := httptest.NewRecorder()
	s.handleEdgeAuthApp(w, r, a)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a valid session", w.Code)
	}
}

// Decrypt hands back anything without the encryption marker unchanged, so a
// hand-written cookie would otherwise parse as a perfectly good session. This
// is the difference between a signed session and none at all.
func TestEdgeSessionRejectsAnUnsealedCookie(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	forged := []string{
		a.ID + "|" + credentialFingerprint(a) + "|9999999999",
		"enc:v1:not-base64",
		"",
	}
	for _, value := range forged {
		r := edgeRequest("/")
		r.AddCookie(&http.Cookie{Name: edgeCookiePrefix + a.ID, Value: value})
		if s.edgeSessionValid(r, a) {
			t.Errorf("cookie %q was accepted as a session", value)
		}
	}
}

// Changing an app's password has to lock out whoever knew the old one now, not
// when their session happens to run out.
func TestEdgeSessionDiesWithTheCredentialsItWasIssuedAgainst(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	token, err := s.edgeToken(a)
	if err != nil {
		t.Fatal(err)
	}
	r := edgeRequest("/")
	r.AddCookie(&http.Cookie{Name: edgeCookiePrefix + a.ID, Value: token})
	if !s.edgeSessionValid(r, a) {
		t.Fatal("the session is not valid to begin with")
	}

	rotated, err := bcrypt.GenerateFromPassword([]byte("newpassword"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a.BasicAuthHash = string(rotated)
	if s.edgeSessionValid(r, a) {
		t.Error("a session opened against the old password survived the change")
	}
}

// A session is for one application. Presented to another it must mean nothing,
// or protecting an app would be undone by having an account on its neighbour.
func TestEdgeSessionIsNotTransferableBetweenApps(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	token, err := s.edgeToken(a)
	if err != nil {
		t.Fatal(err)
	}
	other := edgeApp(t)
	other.ID = "beef0002"
	r := edgeRequest("/")
	r.AddCookie(&http.Cookie{Name: edgeCookiePrefix + other.ID, Value: token})
	if s.edgeSessionValid(r, other) {
		t.Error("another app's session opened this one")
	}
}

// "next" comes from the request, so it is exactly what a phishing link would
// aim somewhere else. Only a path on the app is ever followed.
func TestEdgeSignInNeverRedirectsOffTheApp(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	for _, next := range []string{
		"https://evil.example.com/",
		"//evil.example.com/",
		"http:/\\evil.example.com",
	} {
		r := edgeRequest(edgeAuthPath + "?next=" + next)
		r.SetBasicAuth("ops", "s3cret")
		w := httptest.NewRecorder()
		s.handleEdgeAuthApp(w, r, a)
		got := w.Header().Get("Location")
		if strings.Contains(got, "evil.example.com") {
			t.Errorf("next=%q redirected to %q", next, got)
		}
	}
}

// An expired session must not authorise, however well formed it is.
func TestEdgeSessionExpires(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	stale, err := s.keyring.Encrypt(a.ID + "|" + credentialFingerprint(a) + "|" +
		time.Now().Add(-time.Minute).Format("20060102"))
	if err != nil {
		t.Fatal(err)
	}
	r := edgeRequest("/")
	r.AddCookie(&http.Cookie{Name: edgeCookiePrefix + a.ID, Value: stale})
	if s.edgeSessionValid(r, a) {
		t.Error("an expired session was accepted")
	}
}

// Every wrong guess costs a bcrypt comparison on the machine that runs every
// other application here, so a public login page needs a ceiling.
func TestEdgeThrottleStopsGuessingButNotAWorkingClient(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)

	for i := 0; i < edgeMaxFailures+5; i++ {
		r := edgeRequest("/")
		r.SetBasicAuth("ops", "wrong")
		s.edgeCredentialsInHeader(r, a)
	}
	// The right password from the same address is now refused too: the point is
	// that guessing stops, and a legitimate visitor still has the page.
	r := edgeRequest("/")
	r.SetBasicAuth("ops", "s3cret")
	if s.edgeCredentialsInHeader(r, a) {
		t.Error("the throttle let an eleventh attempt through")
	}
	// Another address is unaffected — one visitor guessing must not lock the
	// application away from everyone else.
	elsewhere := edgeRequest("/")
	elsewhere.Header.Set("X-Forwarded-For", "198.51.100.9")
	elsewhere.SetBasicAuth("ops", "s3cret")
	if !s.edgeCredentialsInHeader(elsewhere, a) {
		t.Error("one address guessing locked out another")
	}
}

// A correct password never counts against the limit: a monitoring probe calling
// a protected app every few seconds would otherwise throttle itself out.
func TestEdgeThrottleIgnoresSuccessfulRequests(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	for i := 0; i < edgeMaxFailures*3; i++ {
		r := edgeRequest("/")
		r.SetBasicAuth("ops", "s3cret")
		if !s.edgeCredentialsInHeader(r, a) {
			t.Fatalf("attempt %d with the right password was refused", i+1)
		}
	}
}

// Signing out has to end the session on the spot.
func TestEdgeSignOutClearsTheCookie(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	r := edgeRequest(edgeAuthPath + "?logout=1")
	w := httptest.NewRecorder()
	s.handleEdgeAuthApp(w, r, a)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Errorf("sign-out did not expire the cookie: %+v", cookies)
	}
}

// An app with no password configured is let through rather than left stranded:
// its container may still carry the middleware from before protection was
// removed, and failing closed there would hold a site hostage to a redeploy.
func TestEdgeAuthLetsAnUnprotectedAppThrough(t *testing.T) {
	s := edgeServer(t)
	open := &db.App{ID: "beef0003", Name: "Blog", Subdomain: "blog"}
	w := httptest.NewRecorder()
	s.handleEdgeAuthApp(w, edgeRequest("/"), open)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an app with no protection", w.Code)
	}
}

// Traefik resolves a relative Location against the address it called for
// authorisation, so a bare "/reports" would send the visitor to the dashboard's
// internal hostname — unreachable from a browser, right after a sign-in that
// worked. The redirect therefore names the application's own host.
func TestEdgeSignInRedirectsToTheApplicationsOwnHost(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	s.cfg.Domain = "example.com"
	a.CustomDomains = "www.acme.test"

	tests := []struct {
		name      string
		forwarded string
		want      string
	}{
		{"the app's canonical host", "api.example.com", "https://api.example.com/reports"},
		{"one of its custom domains", "www.acme.test", "https://www.acme.test/reports"},
		// Never a host of someone else's choosing, whatever the header says.
		{"a host the app does not answer for", "evil.example.net", "https://api.example.com/reports"},
		{"no forwarded host at all", "", "https://api.example.com/reports"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := edgeRequest(edgeAuthPath + "?next=%2Freports")
			r.Header.Set("X-Forwarded-Host", tc.forwarded)
			r.SetBasicAuth("ops", "s3cret")
			w := httptest.NewRecorder()
			s.handleEdgeAuthApp(w, r, a)
			if got := w.Header().Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

// A forwarded host carries a port when the edge does not listen on 443, as it
// does in development. Dropping it would redirect to a port nothing serves.
func TestEdgeSignInKeepsThePortItWasReachedOn(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	s.cfg.Domain = "example.com"
	r := edgeRequest(edgeAuthPath + "?next=%2F")
	r.Header.Set("X-Forwarded-Host", "api.example.com:8443")
	r.SetBasicAuth("ops", "s3cret")
	w := httptest.NewRecorder()
	s.handleEdgeAuthApp(w, r, a)
	if got := w.Header().Get("Location"); got != "https://api.example.com:8443/" {
		t.Errorf("Location = %q, want the port the visitor arrived on kept", got)
	}
}

// The page carries both of the dashboard's typefaces, so it must only be sent
// where it would be looked at. A browser asking a protected app for an image or
// a stylesheet gets the status and nothing else.
func TestEdgeLoginPageOnlyAnswersNavigations(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)

	tests := []struct {
		name     string
		dest     string
		accept   string
		wantPage bool
	}{
		{"a page the visitor navigated to", "document", "", true},
		{"a page inside a frame", "iframe", "", true},
		{"an image on that page", "image", "", false},
		{"a background fetch", "empty", "", false},
		// Older browsers and scripted clients say what they want in Accept.
		{"no Sec-Fetch-Dest, asking for html", "", "text/html,*/*", true},
		{"no Sec-Fetch-Dest, asking for json", "", "application/json", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := edgeRequest("/")
			r.Header.Del("Sec-Fetch-Dest")
			if tc.dest != "" {
				r.Header.Set("Sec-Fetch-Dest", tc.dest)
			}
			r.Header.Set("Accept", tc.accept)
			w := httptest.NewRecorder()
			s.handleEdgeAuthApp(w, r, a)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 either way", w.Code)
			}
			if got := strings.Contains(w.Body.String(), "<form"); got != tc.wantPage {
				t.Errorf("page rendered = %v, want %v (body %d bytes)", got, tc.wantPage, w.Body.Len())
			}
		})
	}
}

// The page is served where "/static/..." belongs to the application and a font
// CDN may be unreachable, so it has to carry the dashboard's typefaces itself —
// otherwise it silently falls back to the visitor's system fonts and stops
// looking like Quasar.
func TestEdgeLoginPageCarriesItsOwnTypefaces(t *testing.T) {
	s, a := edgeServer(t), edgeApp(t)
	w := httptest.NewRecorder()
	s.edgeLoginPage(w, edgeRequest("/"), a, "/", false)
	body := w.Body.String()

	for _, family := range []string{"Space Grotesk", "JetBrains Mono"} {
		if !strings.Contains(body, family) {
			t.Errorf("the page is not set in %s:\n%s", family, body[:min(len(body), 800)])
		}
	}
	if strings.Count(body, "data:font/woff2;base64,") != 2 {
		t.Errorf("want both typefaces inlined, found %d", strings.Count(body, "data:font/woff2;base64,"))
	}
	// The accent, the corner radius and the mark are what make it Quasar's.
	for _, token := range []string{"#d4e815", "--radius: 4px", "<svg viewBox=\"0 0 24 24\""} {
		if !strings.Contains(body, token) {
			t.Errorf("the page does not carry %q", token)
		}
	}
}
