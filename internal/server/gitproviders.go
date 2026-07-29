package server

// gitProvider is the setup guidance for one forge: where its tokens are made,
// the least the token has to be allowed to do, and what it expects in the
// username field.
//
// Getting a token wrong is the single most common way a private repository
// fails to deploy, and the failure — an authentication error — says nothing
// about which of the three went wrong. Putting the answers next to the form
// costs a page of static text and removes a support round trip.
type gitProvider struct {
	Name string
	// Host is the forge's domain, offered as the starting point for a
	// credential's scope. Empty for a forge that is normally self-hosted,
	// where only the operator knows the domain.
	Host string
	// TokenURL is where the token is created; a path when the forge is
	// self-hosted, since the domain varies.
	TokenURL string
	// Scope is the permission to grant, named the way that forge names it.
	Scope string
	// Username is what the credential's username field needs, and why.
	Username string
	Note     string
}

// gitProviders is ordered by how often a self-hosted platform meets them.
var gitProviders = []gitProvider{
	{
		Name:     "GitHub",
		Host:     "github.com",
		TokenURL: "https://github.com/settings/personal-access-tokens",
		Scope:    "Repository access: the repositories to deploy · Permissions → Contents: Read-only",
		Username: "Leave empty",
		Note: "A fine-grained token belongs to one account or organisation, so pair it with a " +
			"scope of github.com/<that owner> and a second credential can cover the rest. " +
			"They also expire, and a deploy that worked for months stops with an authentication " +
			"error the day they do — set a reminder, or use a classic token " +
			"(github.com/settings/tokens) with the repo scope instead.",
	},
	{
		Name:     "GitLab",
		Host:     "gitlab.com",
		TokenURL: "https://gitlab.com/-/user_settings/personal_access_tokens",
		Scope:    "read_repository",
		Username: "Leave empty",
		Note: "A project or group access token works too and is the narrower choice: " +
			"Settings → Access tokens on the project itself, with the Reporter role. " +
			"Scope it to gitlab.com/<group> so it is only ever offered to that group's projects.",
	},
	{
		Name:     "Bitbucket",
		Host:     "bitbucket.org",
		TokenURL: "https://bitbucket.org/account/settings/app-passwords/",
		Scope:    "Repositories: Read",
		Username: "Your Bitbucket username — required",
		Note: "The only forge here that checks the username. It is the account name from " +
			"your profile, not your email address; an app password on its own is refused.",
	},
	{
		Name:     "Gitea / Forgejo",
		TokenURL: "/user/settings/applications",
		Scope:    "read:repository",
		Username: "Leave empty",
		Note: "Self-hosted, so open that path on your own instance. Scope the credential to " +
			"the instance's domain, with its port if it serves on one.",
	},
	{
		Name:     "Azure DevOps",
		Host:     "dev.azure.com",
		TokenURL: "https://dev.azure.com/_usersSettings/tokens",
		Scope:    "Code: Read",
		Username: "Leave empty",
		Note:     "Repository URLs are https://dev.azure.com/<org>/<project>/_git/<repo>.",
	},
}
