package catalog

var radarr = Template{
	ID: "radarr", Name: "Radarr", Description: "Film library manager",
	Category: "Downloads", ImageRef: "lscr.io/linuxserver/radarr:latest", Port: 7878, DataMount: "/config",
	Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
}
