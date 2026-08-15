package catalog

var forgejo = Template{
	ID: "forgejo", Name: "Forgejo", Description: "Community fork of Gitea",
	Category: "Development", ImageRef: "codeberg.org/forgejo/forgejo:11", Port: 3000, DataMount: "/data",
	Env: "USER_UID=1000\nUSER_GID=1000",
}
