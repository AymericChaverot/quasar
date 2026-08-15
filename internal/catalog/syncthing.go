package catalog

var syncthing = Template{
	ID: "syncthing", Name: "Syncthing", Description: "Continuous file sync between devices",
	Category: "Files & sync", ImageRef: "syncthing/syncthing:latest", Port: 8384, DataMount: "/var/syncthing",
	Note: "The web UI is routed. Device-to-device sync needs TCP/UDP 22000, which Traefik does not carry.",
}
