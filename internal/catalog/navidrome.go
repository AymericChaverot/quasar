package catalog

var navidrome = Template{
	ID: "navidrome", Name: "Navidrome", Description: "Music streaming server",
	Category: "Media", ImageRef: "deluan/navidrome:latest", Port: 4533, DataMount: "/data",
}
