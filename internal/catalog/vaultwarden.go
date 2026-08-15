package catalog

var vaultwarden = Template{
	ID: "vaultwarden", Name: "Vaultwarden", Description: "Bitwarden-compatible password manager",
	Category: "Security", ImageRef: "vaultwarden/server:latest", Port: 80, DataMount: "/data",
	Env: "ADMIN_TOKEN={{RANDOM}}{{RANDOM}}\nSIGNUPS_ALLOWED=false",
}
