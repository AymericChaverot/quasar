package catalog

var authelia = Template{
	ID: "authelia", Name: "Authelia", Description: "Authentication gateway with 2FA",
	Category: "Security", ImageRef: "authelia/authelia:latest", Port: 9091, DataMount: "/config",
	NeedsSetup: "Authelia writes a default configuration.yml into its data directory on first run and then exits, " +
		"because the identity provider, users and access rules are yours to define. Edit that file, then redeploy. " +
		"For single sign-on that comes up on its own, Authentik is in this catalogue too.",
}
