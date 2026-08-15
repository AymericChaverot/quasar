package catalog

var sonarr = Template{
	ID: "sonarr", Name: "Sonarr", Description: "TV series library manager",
	Category: "Downloads", ImageRef: "lscr.io/linuxserver/sonarr:latest", Port: 8989, DataMount: "/config",
	Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
}
