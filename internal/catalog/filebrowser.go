package catalog

var filebrowser = Template{
	ID: "filebrowser", Name: "File Browser", Description: "Web file manager",
	Category: "Files & sync", ImageRef: "filebrowser/filebrowser:latest", Port: 80, DataMount: "/srv",
}
