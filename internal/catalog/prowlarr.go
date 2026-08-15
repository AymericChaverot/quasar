package catalog

var prowlarr = Template{
	ID: "prowlarr", Name: "Prowlarr", Description: "Indexer manager for the *arr apps",
	Category: "Downloads", ImageRef: "lscr.io/linuxserver/prowlarr:latest", Port: 9696, DataMount: "/config",
	Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
}
