package catalog

var homepage = Template{
	ID: "homepage", Name: "Homepage", Description: "Start page for your services",
	Category: "Dashboards & monitoring", ImageRef: "ghcr.io/gethomepage/homepage:latest", Port: 3000, DataMount: "/app/config",
	Env:  "HOMEPAGE_ALLOWED_HOSTS={{HOST}}",
	Note: "Homepage answers 400 to any host it was not told about; HOMEPAGE_ALLOWED_HOSTS below is set from the subdomain, so update it if you change that.",
}
