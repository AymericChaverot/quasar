package catalog

var qbittorrent = Template{
	ID: "qbittorrent", Name: "qBittorrent", Description: "BitTorrent client with a web UI",
	Category: "Downloads", ImageRef: "lscr.io/linuxserver/qbittorrent:latest", Port: 8080, DataMount: "/config",
	Env: "PUID=1000\nPGID=1000\nTZ=Etc/UTC\nWEBUI_PORT=8080",
}
