package docker

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"quasar/internal/db"
)

// askPassScript answers git's credential prompts from the environment.
//
// git calls this once per prompt with the prompt text as its first argument,
// which is the only way to tell which of the two it wants. The values travel
// in environment variables so the token is never in a command line — visible
// to every process on the host through /proc — and never in .git/config, which
// is what a clone from an authenticated URL leaves behind on disk.
const askPassScript = `#!/bin/sh
case "$1" in
	Username*) printf '%s\n' "$QUASAR_GIT_USERNAME" ;;
	*)         printf '%s\n' "$QUASAR_GIT_PASSWORD" ;;
esac
`

// askPassHelper writes the credential helper and returns its path. The script
// holds no secret, so one copy serves every deploy and outliving the request
// costs nothing.
func (c *Client) askPassHelper() (string, error) {
	c.askPassOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "quasar-git-askpass.sh")
		if err := os.WriteFile(path, []byte(askPassScript), 0o700); err != nil {
			c.askPassErr = fmt.Errorf("install git credential helper: %w", err)
			return
		}
		c.askPassPath = path
	})
	return c.askPassPath, c.askPassErr
}

// gitErrLines is how much of a failed git command's output is quoted back.
const gitErrLines = 20

// gitRun runs a git command that may need to authenticate against repoURL,
// passing each line it prints to out as it arrives (out may be nil).
//
// Credentials are resolved from the URL's host and handed over through the
// environment; the arguments carry the URL exactly as the operator wrote it.
// Terminal prompting is off, so a rejected token fails immediately instead of
// blocking on a username nobody is there to type.
func (c *Client) gitRun(ctx context.Context, out func(string), repoURL string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	env, cred, err := c.gitEnv(repoURL)
	if err != nil {
		return err
	}
	cmd.Env = env

	sink := &lineSink{emit: out, keep: gitErrLines}
	cmd.Stdout, cmd.Stderr = sink, sink
	err = cmd.Run()
	sink.flush()
	if err != nil {
		return fmt.Errorf("git: %s: %w", redactURLs(sink.text()), err)
	}
	if cred != nil {
		// Recorded only on success, so "last used" means the token worked.
		if err := db.MarkGitCredentialUsed(c.dbc, cred.ID); err != nil {
			log.Printf("git: marking credential %d used: %v", cred.ID, err)
		}
	}
	return nil
}

// gitStripped are the variables dropped from the environment a git command
// inherits, so that what authenticates a deploy is only ever what Quasar put
// there.
//
// The host has its own git plumbing: a developer's credential manager, a
// terminal or an editor that exports GIT_ASKPASS, an agent's SSH_ASKPASS.
// Inherited, any of those would be consulted during a clone Quasar believes is
// anonymous — prompting a helper nobody is watching, or answering with an
// account that is not the one the operator configured here. The two QUASAR_
// variables are stripped for the mirror-image reason: they must mean something
// only when gitEnv has just set them, never because they were already lying
// around in the process's environment.
var gitStripped = map[string]bool{
	"GIT_ASKPASS":         true,
	"SSH_ASKPASS":         true,
	"QUASAR_GIT_USERNAME": true,
	"QUASAR_GIT_PASSWORD": true,
	"GIT_TERMINAL_PROMPT": true, // set below; stripped so it cannot be duplicated
	"GCM_INTERACTIVE":     true,
}

// gitBaseEnv is the inherited environment with that plumbing removed and
// Quasar's own settings applied.
func gitBaseEnv() []string {
	host := os.Environ()
	env := make([]string, 0, len(host)+2)
	for _, kv := range host {
		name, _, _ := strings.Cut(kv, "=")
		// Upper-cased because Windows environment names are case-insensitive,
		// and a "Git_AskPass" there is the same variable git will read.
		if gitStripped[strings.ToUpper(name)] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
}

// gitEnv builds the environment a git command runs in, and reports which
// credential (if any) it will offer.
func (c *Client) gitEnv(repoURL string) ([]string, *db.GitCredential, error) {
	env := gitBaseEnv()
	cred := c.gitCredentialFor(repoURL)
	if cred == nil {
		return env, nil, nil
	}
	helper, err := c.askPassHelper()
	if err != nil {
		return nil, nil, err
	}
	return append(env,
		"GIT_ASKPASS="+helper,
		"QUASAR_GIT_USERNAME="+cred.Account(),
		"QUASAR_GIT_PASSWORD="+cred.Secret,
	), cred, nil
}

// gitCredentialFor resolves what would authenticate a clone of rawURL, or nil
// when nothing applies.
//
// The whole URL decides, not just its host: that is what lets one GitHub
// account's token cover one organisation and another's cover the next, and
// what stops any forge's token from ever being offered to another.
func (c *Client) gitCredentialFor(rawURL string) *db.GitCredential {
	// Checked before any lookup: an ssh remote, a local path, or a URL that
	// already carries credentials has no use for a token — and this is what
	// lets a deploy from a plain path run without a database at all.
	if !strings.HasPrefix(rawURL, "https://") || strings.Contains(rawURL, "@") {
		return nil
	}
	if c.dbc == nil || c.keyring == nil {
		return nil
	}
	return db.GitCredentialFor(c.dbc, c.keyring, rawURL)
}

// credentialInURL matches the userinfo of any URL: scheme, then everything up
// to the @ that closes it.
var credentialInURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s@]*@`)

// redactURLs strips credentials out of text on its way to a deploy log, a
// notification or the browser.
//
// Nothing Quasar builds carries a token in a URL any more, but a repository's
// own submodule or an operator who pasted credentials into the clone URL still
// can, and git reports the remote it failed to reach verbatim.
func redactURLs(s string) string {
	return credentialInURL.ReplaceAllString(s, "${1}***@")
}

// CheckGitAccess reports whether Quasar can reach a repository with what it
// has stored, by doing what a deploy does and nothing more.
//
// This runs the real authentication path rather than calling a forge's API: a
// token that lists fine through an API and still cannot clone — the usual
// shape of a missing scope — is exactly the failure worth catching here.
func (c *Client) CheckGitAccess(ctx context.Context, repoURL string) (string, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", fmt.Errorf("no repository URL to test against")
	}
	cred := c.gitCredentialFor(repoURL)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.gitRun(ctx, nil, repoURL, "ls-remote", "--heads", repoURL); err != nil {
		return "", fmt.Errorf("%s", firstLine(err.Error()))
	}
	if cred == nil {
		return "Reachable with no credential at all — this repository is public.", nil
	}
	return fmt.Sprintf("Authenticated against %s as %s.", db.GitHostOf(repoURL), cred.Account()), nil
}

// firstLine keeps a git failure to the line that says what went wrong. The
// rest is advice aimed at someone sitting at a terminal.
func firstLine(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(s, "git: "))
	if s == "" {
		return "git failed without saying why"
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
