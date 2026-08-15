package catalog

var calibreWeb = Template{
	ID: "calibre-web", Name: "Calibre-Web", Description: "Browse and read an ebook library",
	Category: "Media", ImageRef: "lscr.io/linuxserver/calibre-web:latest", Port: 8083, DataMount: "/config",
	Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC",
}
