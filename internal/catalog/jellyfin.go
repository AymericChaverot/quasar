package catalog

var jellyfin = Template{
	ID: "jellyfin", Name: "Jellyfin", Description: "Movies, TV and music streaming",
	Category: "Media", ImageRef: "jellyfin/jellyfin:latest", Port: 8096, DataMount: "/config",
}
