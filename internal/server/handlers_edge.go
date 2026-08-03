package server

// Password protection in front of an application, as seen by its visitors.
//
// Traefik does not check the password itself: the app's router carries a
// forward-auth middleware pointing here, and every request to a protected app
// becomes a call to handleEdgeAuth first. Answering 2xx lets the request
// through to the app, anything else is returned to the visitor as-is — which is
// what makes a real login page possible. Traefik's own basicauth middleware can
// only answer with WWW-Authenticate, and the box the browser then draws cannot
// be styled, explained, or branded.
//
// The cost of that choice is a dependency: a protected app needs this dashboard
// to be up. An unprotected one carries no such middleware and never calls here.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quasar/internal/db"
	"quasar/internal/secrets"
	"quasar/web"
)

const (
	// edgeAuthPath is the path on the application's own domain that the login
	// page posts to. Requests to it are answered here and never reach the app,
	// so it only has to be a path no application would want for itself.
	edgeAuthPath = "/__quasar-auth"
	// edgeSessionTTL is how long a visitor stays signed in to an application.
	edgeSessionTTL = 7 * 24 * time.Hour
	// edgeCookiePrefix names the session cookie. The app's ID is appended, so
	// two protected apps on neighbouring subdomains keep separate sessions.
	edgeCookiePrefix = "qs_edge_"
)

// handleEdgeAuth is what Traefik calls before serving a protected application.
//
// It is deliberately reachable without a Quasar session: the visitor being
// authorised here is not a user of the dashboard, and the credentials are the
// app's own. What it can be made to do is bounded by that — it authorises one
// application, against one password, and returns nothing about either.
func (s *Server) handleEdgeAuth(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		// An app that is gone cannot be protected. Its middleware outliving it
		// is the deploy's business, not a reason to hold a request.
		w.WriteHeader(http.StatusOK)
		return
	}
	s.handleEdgeAuthApp(w, r, a)
}

// handleEdgeAuthApp is handleEdgeAuth once the application is known.
func (s *Server) handleEdgeAuthApp(w http.ResponseWriter, r *http.Request, a *db.App) {
	// An app whose protection was removed since its container was created is
	// let through: failing closed would strand a site behind a password its
	// operator has already deleted, until they thought to redeploy.
	if a.BasicAuthUser == "" || a.BasicAuthHash == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The request being authorised, as the visitor made it. Traefik describes
	// it in headers, because the call it makes here is always a GET to this
	// address whatever the visitor did.
	target := r.Header.Get("X-Forwarded-Uri")

	if strings.HasPrefix(pathOf(target), edgeAuthPath) {
		s.edgeSignIn(w, r, a, target)
		return
	}
	if s.edgeSessionValid(r, a) || s.edgeCredentialsInHeader(r, a) {
		w.WriteHeader(http.StatusOK)
		return
	}
	s.edgeLoginPage(w, r, a, target, false)
}

// edgeSignIn answers the requests the login page itself makes: signing in with
// the credentials it collected, and signing out again.
//
// Every answer here is deliberately outside 2xx. A 2xx would have Traefik
// forward this request on to the application, which knows nothing of these
// paths — and, worse, would drop the Set-Cookie that is the whole point.
func (s *Server) edgeSignIn(w http.ResponseWriter, r *http.Request, a *db.App, target string) {
	query := queryOf(target)

	if query.Get("logout") != "" {
		http.SetCookie(w, s.edgeCookie(a, "", -1))
		s.redirect(w, r, a, "/")
		return
	}

	// Credentials arrive in the Authorization header rather than a form body:
	// Traefik only forwards a request body to the auth server when told to,
	// and telling it to would mean buffering every upload to every protected
	// app — and answering 401 to any that outgrew the configured limit.
	if !s.edgeCredentialsInHeader(r, a) {
		s.edgeLoginPage(w, r, a, query.Get("next"), true)
		return
	}
	token, err := s.edgeToken(a)
	if err != nil {
		http.Error(w, "could not start a session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, s.edgeCookie(a, token, edgeSessionTTL))
	s.redirect(w, r, a, pathOf(query.Get("next")))
}

// edgeLoginView is what the login page is rendered from. It carries the app's
// name and hostname and nothing else about it: this page is served to whoever
// asks, so everything on it is public by construction.
type edgeLoginView struct {
	Name      string
	Host      string
	Denied    bool
	SignInURL string
	// Fonts are the dashboard's two typefaces, inlined. See edgeFontFaces.
	Fonts template.CSS
}

// edgeFontFaces is the @font-face block for the login page, with both typefaces
// embedded as data URIs.
//
// The page is served on the application's own domain, where "/static/..." is
// the application's to answer and a font CDN may be unreachable — the servers
// Quasar runs on often have no route to the public internet, which is why the
// dashboard ships its fonts inside the binary in the first place. Inlining them
// is what keeps this page looking like the rest of Quasar rather than falling
// back to whatever the visitor's system provides.
//
// Built once, from the same files the dashboard is set in, so the two cannot
// drift apart. The italic cut is left out: it exists for log output.
var edgeFontFaces = sync.OnceValue(func() template.CSS {
	var out strings.Builder
	fonts := []struct{ family, file string }{
		{"Space Grotesk", "static/fonts/space-grotesk-latin-wght-normal.woff2"},
		{"JetBrains Mono", "static/fonts/jetbrains-mono-latin-wght-normal.woff2"},
	}
	for _, f := range fonts {
		data, err := web.Files.ReadFile(f.file)
		if err != nil {
			// The page is readable in the fallback faces; a gate that would not
			// render at all because a font is missing would be far worse.
			log.Printf("edge login page: %v", err)
			continue
		}
		fmt.Fprintf(&out, "@font-face{font-family:%q;src:url(data:font/woff2;base64,%s) format(\"woff2\");font-weight:100 800;font-style:normal;font-display:swap}\n",
			f.family, base64.StdEncoding.EncodeToString(data))
	}
	return template.CSS(out.String())
})

// edgeLoginPage answers with the login page and a 401, which is both the honest
// status for "not authorised" and what makes Traefik return this page to the
// visitor instead of forwarding the request to the application.
func (s *Server) edgeLoginPage(w http.ResponseWriter, r *http.Request, a *db.App, target string, denied bool) {
	// Only a page the visitor is actually looking at gets the page. Everything
	// else a browser asks a protected app for — an image, a stylesheet, a
	// background fetch — gets the bare status: sending a whole document with
	// two embedded typefaces in answer to a request for a favicon is waste, and
	// nothing would ever render it.
	if !wantsDocument(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	view := edgeLoginView{
		Name:      a.Name,
		Host:      appHost(a, s.cfg.Domain),
		Denied:    denied,
		SignInURL: edgeAuthPath + "?next=" + url.QueryEscape(pathOf(target)),
		Fonts:     edgeFontFaces(),
	}
	// No WWW-Authenticate header: it is what makes the browser draw its own
	// credentials box over this page, which is the thing being replaced.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	if err := s.pages["dashboard"].ExecuteTemplate(w, "edge_login_page", view); err != nil {
		log.Printf("render edge login page: %v", err)
	}
}

// wantsDocument reports whether the request is a browser navigating to a page,
// as opposed to fetching something to put on one.
//
// Sec-Fetch-Dest says so outright and every current browser sends it; the
// Accept header is the fallback for the ones that do not, and for anything
// scripted, which asks for the status and not for a page anyway.
func wantsDocument(r *http.Request) bool {
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return dest == "document" || dest == "iframe" || dest == "frame"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// redirect sends the visitor on with a 303, whose status is what makes Traefik
// hand the whole response — cookie included — back to the browser.
//
// The Location is absolute, and has to be: Traefik resolves a relative one
// against the address it called for authorisation, so "/reports" would reach
// the visitor as the dashboard's own internal address — a host their browser
// cannot resolve, on the way back from a sign-in that worked. Traefik can be
// told to leave the header alone, but only through a middleware option younger
// than some of the v3 releases this may be running against.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, a *db.App, path string) {
	// A path, and unmistakably a path. "//host" is protocol-relative, and
	// browsers read a backslash there the same way — "/\evil.example.com" is a
	// redirect off the site, however much it looks like a path.
	if len(path) < 1 || path[0] != '/' || (len(path) > 1 && (path[1] == '/' || path[1] == '\\')) {
		path = "/"
	}
	scheme := "https"
	if r.Header.Get("X-Forwarded-Proto") == "http" {
		scheme = "http"
	}
	w.Header().Set("Location", scheme+"://"+s.edgeHost(r, a)+path)
	w.WriteHeader(http.StatusSeeOther)
}

// edgeHost is the hostname to send the visitor back to: the one they arrived
// on, as long as it is one this application answers for, and its canonical name
// otherwise.
//
// Traefik only calls here after matching the app's own router, so the forwarded
// host is already one of these. Checking anyway costs nothing, and keeps the
// single header-derived value in the redirect from being worth bending.
func (s *Server) edgeHost(r *http.Request, a *db.App) string {
	canonical := appHost(a, s.cfg.Domain)
	forwarded := r.Header.Get("X-Forwarded-Host")
	// Compared without the port, kept with it: Quasar serves on 443 and a
	// browser leaves that out, but a development edge on another port has to
	// come back to that same port or the redirect lands nowhere.
	bare := forwarded
	if host, _, err := net.SplitHostPort(forwarded); err == nil {
		bare = host
	}
	for _, known := range append([]string{canonical}, a.CustomDomainList()...) {
		if bare == known {
			return forwarded
		}
	}
	return canonical
}

// edgeCredentialsInHeader reports whether the request carries the app's
// credentials in an Authorization header.
//
// The login page puts them there, and so does anything scripted: `curl -u`, a
// webhook, a monitoring probe. Keeping that spelling working is why the switch
// to a login page does not break every non-browser client of a protected app.
func (s *Server) edgeCredentialsInHeader(r *http.Request, a *db.App) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	key := a.ID + "|" + clientIP(r)
	if !s.edgeAttempts.allow(key) {
		return false
	}
	// The username is compared in constant time like the password. It is not a
	// secret, but a timing difference on it would say which of the two was
	// wrong, and answering that question is not the login page's job.
	nameOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.BasicAuthUser)) == 1
	// The hash is computed either way, so a wrong username does not return
	// faster than a wrong password.
	passOK := bcrypt.CompareHashAndPassword([]byte(a.BasicAuthHash), []byte(pass)) == nil
	if !nameOK || !passOK {
		s.edgeAttempts.failed(key)
		return false
	}
	return true
}

// edgeToken is the value of an app's session cookie: the app it is for, the
// credentials it was issued against, and when it stops being valid, sealed with
// the platform's master key.
//
// The credentials go in as a fingerprint so that changing an app's password
// ends every session opened with the old one. Locking someone out has to mean
// locking them out now, not in seven days.
func (s *Server) edgeToken(a *db.App) (string, error) {
	return s.keyring.Encrypt(fmt.Sprintf("%s|%s|%d", a.ID, credentialFingerprint(a), time.Now().Add(edgeSessionTTL).Unix()))
}

// edgeSessionValid reports whether the request carries a session cookie this
// dashboard issued, for this app, against the password it has now.
func (s *Server) edgeSessionValid(r *http.Request, a *db.App) bool {
	cookie, err := r.Cookie(edgeCookiePrefix + a.ID)
	if err != nil {
		return false
	}
	// Decrypt returns anything without the encryption marker unchanged, so a
	// cookie value made up by hand would otherwise "decrypt" to itself and be
	// parsed as a perfectly good session. This is the check that stops that.
	if !secrets.IsEncrypted(cookie.Value) {
		return false
	}
	opened, err := s.keyring.Decrypt(cookie.Value)
	if err != nil {
		return false
	}
	parts := strings.Split(opened, "|")
	if len(parts) != 3 {
		return false
	}
	var expiry int64
	if _, err := fmt.Sscanf(parts[2], "%d", &expiry); err != nil {
		return false
	}
	return parts[0] == a.ID &&
		subtle.ConstantTimeCompare([]byte(parts[1]), []byte(credentialFingerprint(a))) == 1 &&
		time.Now().Unix() < expiry
}

// credentialFingerprint identifies the credentials a session was opened
// against, without carrying them. The bcrypt hash embeds its own salt, so a
// password set twice fingerprints differently both times.
func credentialFingerprint(a *db.App) string {
	sum := sha256.Sum256([]byte(a.BasicAuthUser + ":" + a.BasicAuthHash))
	return fmt.Sprintf("%x", sum[:8])
}

func (s *Server) edgeCookie(a *db.App, value string, ttl time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:  edgeCookiePrefix + a.ID,
		Value: value,
		Path:  "/",
		// No Domain: the cookie stays on the exact host the visitor signed in
		// to, and is not offered to the app's siblings on other subdomains.
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if ttl < 0 {
		c.MaxAge = -1
		return c
	}
	c.Expires = time.Now().Add(ttl)
	c.MaxAge = int(ttl.Seconds())
	return c
}

// pathOf reduces a URL or request URI to its path, dropping any host and query
// somebody may have put in it.
func pathOf(raw string) string {
	if raw == "" {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return "/"
	}
	return u.Path
}

func queryOf(raw string) url.Values {
	u, err := url.Parse(raw)
	if err != nil {
		return url.Values{}
	}
	return u.Query()
}

// edgeThrottle limits how often one address may guess at one app's password.
//
// Traefik used to reject a wrong password itself; now every guess costs this
// dashboard a bcrypt comparison, which is deliberately slow. Without a ceiling
// that turns a public login page into a way to load the machine that runs
// every other application on it.
// Only failures are counted. A monitoring probe calling a protected app every
// few seconds with the right credentials must never throttle itself.
type edgeThrottle struct {
	mu       sync.Mutex
	failures map[string]*edgeFailures
}

type edgeFailures struct {
	count int
	until time.Time
}

const (
	edgeMaxFailures = 10
	edgeWindow      = time.Minute
)

func newEdgeThrottle() *edgeThrottle {
	return &edgeThrottle{failures: map[string]*edgeFailures{}}
}

// allow reports whether a caller has any attempts left in the current window.
func (t *edgeThrottle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune()
	f := t.failures[key]
	return f == nil || f.count < edgeMaxFailures
}

// failed records a wrong guess, and starts the window if this is the first.
func (t *edgeThrottle) failed(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune()
	if f := t.failures[key]; f != nil {
		f.count++
		return
	}
	t.failures[key] = &edgeFailures{count: 1, until: time.Now().Add(edgeWindow)}
}

// prune drops windows that have run out. Doing it on the way past rather than
// from a goroutine keeps the map only as large as the addresses that guessed
// wrong in the last minute. Callers hold the lock.
func (t *edgeThrottle) prune() {
	now := time.Now()
	for key, f := range t.failures {
		if now.After(f.until) {
			delete(t.failures, key)
		}
	}
}
