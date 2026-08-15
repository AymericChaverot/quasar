package catalog

var registry = Template{
	ID: "registry", Name: "Docker Registry", Description: "Private container image registry",
	Category: "Development", ImageRef: "registry:2", Port: 5000, DataMount: "/var/lib/registry",
	Note: "Put basic auth on this app before pushing to it — an open registry is an open disk.",
}
