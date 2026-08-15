package catalog

var audiobookshelf = Template{
	ID: "audiobookshelf", Name: "Audiobookshelf", Description: "Audiobooks and podcasts",
	Category: "Media", ImageRef: "ghcr.io/advplyr/audiobookshelf:latest", Port: 80, DataMount: "/config",
}
