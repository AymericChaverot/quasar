package catalog

var gitea = Template{
	ID: "gitea", Name: "Gitea", Description: "Git hosting with issues and CI",
	Category: "Development", ImageRef: "gitea/gitea:latest", Port: 3000, DataMount: "/data",
	Env:  "USER_UID=1000\nUSER_GID=1000\nGITEA__server__ROOT_URL={{URL}}/",
	Note: "SSH cloning needs port 22, which Traefik does not carry. HTTPS cloning works over the subdomain.",
}
