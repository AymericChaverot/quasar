package catalog

var sabnzbd = Template{
	ID: "sabnzbd", Name: "SABnzbd", Description: "Usenet downloader",
	Category: "Downloads", ImageRef: "lscr.io/linuxserver/sabnzbd:latest", Port: 8080, DataMount: "/config",
	Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
}
