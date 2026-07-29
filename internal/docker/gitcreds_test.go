package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

func credClient(t *testing.T) *Client {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	k, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []db.GitCredential{
		{Name: "github", Host: "github.com", Secret: "ghp_token"},
		{Name: "gitlab", Host: "gitlab.com", Username: "oauth2", Secret: "glpat-token"},
		{Name: "bitbucket", Host: "bitbucket.org", Username: "alice", Secret: "app pass/word"},
	} {
		if err := db.SaveGitCredential(database, k, &c); err != nil {
			t.Fatal(err)
		}
	}
	return &Client{dbc: database, keyring: k}
}

// A token issued for one forge must never be sent to another. Host matching is
// what enforces that, and it is enforced here rather than at the page.
func TestGitCredentialForPicksTheHostsOwnCredential(t *testing.T) {
	c := credClient(t)

	for _, tc := range []struct{ name, url, want string }{
		{"github's token for a github repo", "https://github.com/owner/repo.git", "ghp_token"},
		{"gitlab's token for a gitlab repo", "https://gitlab.com/g/r.git", "glpat-token"},
		{"a forge with no credential stays anonymous", "https://codeberg.org/o/r.git", ""},
	} {
		got := ""
		if cred := c.gitCredentialFor(tc.url); cred != nil {
			got = cred.Secret
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The cheap checks have to come first, so a deploy from a path or an ssh
// remote never touches the database — which is also what lets syncSource be
// tested against a local repository with no database at all.
func TestGitCredentialForIgnoresURLsItCannotAuthenticate(t *testing.T) {
	c := credClient(t)
	for _, url := range []string{
		"git@github.com:owner/repo.git",
		"ssh://git@github.com/owner/repo.git",
		"https://oauth2:already@github.com/owner/repo.git",
		"/srv/repos/local.git",
	} {
		if cred := c.gitCredentialFor(url); cred != nil {
			t.Errorf("%s resolved to a credential; it must be left exactly as written", url)
		}
	}
}

// The token reaches git through the environment and nowhere else: not in the
// arguments, where every process on the host could read it out of /proc, and
// not in the URL, which git would copy into .git/config.
func TestGitEnvCarriesTheSecretAndTheArgumentsDoNot(t *testing.T) {
	c := credClient(t)
	const url = "https://bitbucket.org/team/repo.git"

	env, cred, err := c.gitEnv(url)
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil {
		t.Fatal("bitbucket.org should resolve to a credential")
	}
	want := map[string]bool{
		"QUASAR_GIT_USERNAME=alice":         false,
		"QUASAR_GIT_PASSWORD=app pass/word": false,
		"GIT_TERMINAL_PROMPT=0":             false,
	}
	askPass := false
	for _, e := range env {
		if _, ok := want[e]; ok {
			want[e] = true
		}
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			askPass = true
		}
	}
	for e, found := range want {
		if !found {
			t.Errorf("environment is missing %q", e)
		}
	}
	if !askPass {
		t.Error("no GIT_ASKPASS: git would have no way to answer a credential prompt")
	}

	// A public repository is run with no credential in the environment at all.
	env, cred, err = c.gitEnv("https://codeberg.org/o/r.git")
	if err != nil || cred != nil {
		t.Fatalf("public repo resolved to %v (err %v)", cred, err)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "QUASAR_GIT_PASSWORD=") || strings.HasPrefix(e, "GIT_ASKPASS=") {
			t.Errorf("a public clone should carry no credential, got %q", e)
		}
	}
}

// The helper is what git executes; if it ever stops answering the two prompts
// correctly, every private deploy hangs or fails at once.
func TestAskPassHelperAnswersBothPrompts(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell to run the helper with")
	}
	c := credClient(t)
	helper, err := c.askPassHelper()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ prompt, want string }{
		{"Username for 'https://github.com': ", "alice"},
		{"Password for 'https://alice@github.com': ", "s3cret"},
	} {
		cmd := exec.Command("sh", helper, tc.prompt)
		cmd.Env = append(os.Environ(), "QUASAR_GIT_USERNAME=alice", "QUASAR_GIT_PASSWORD=s3cret")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", tc.prompt, err)
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("prompt %q answered %q, want %q", tc.prompt, got, tc.want)
		}
	}
}

// The one place a token reliably ended up in plain sight was the log of the
// deploy that could not use it: git names the remote it failed to reach, and
// for a private clone that remote is the URL Quasar built.
func TestRedactURLsStripsCredentialsFromGitOutput(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a failed clone naming the remote",
			"fatal: could not read from 'https://oauth2:ghp_secret@github.com/o/r.git'",
			"fatal: could not read from 'https://***@github.com/o/r.git'"},
		{"several on one line",
			"https://a:b@x.com and https://c:d@y.com",
			"https://***@x.com and https://***@y.com"},
		{"a URL that carries no credentials",
			"fatal: repository 'https://github.com/o/r.git' not found",
			"fatal: repository 'https://github.com/o/r.git' not found"},
		{"an email address is not a URL",
			"Committer: someone@example.com",
			"Committer: someone@example.com"},
		{"ssh remotes too",
			"ssh://git:key@example.com/r.git",
			"ssh://***@example.com/r.git"},
	} {
		if got := redactURLs(tc.in); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestFirstLineKeepsTheFailureNotTheAdvice(t *testing.T) {
	in := "remote: Support for password authentication was removed.\nfatal: Authentication failed\nhint: See the docs"
	if got := firstLine(in); got != "remote: Support for password authentication was removed." {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine("   "); got != "git failed without saying why" {
		t.Errorf("empty output = %q, want a usable message", got)
	}
}
