package catalog

var jellyseerr = Template{
	ID: "jellyseerr", Name: "Jellyseerr", Description: "Media requests for Jellyfin and Plex",
	Category: "Downloads", ImageRef: "fallenbagel/jellyseerr:latest", Port: 5055, DataMount: "/app/config",
}
