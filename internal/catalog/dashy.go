package catalog

var dashy = Template{
	ID: "dashy", Name: "Dashy", Description: "Configurable service dashboard",
	Category: "Dashboards & monitoring", ImageRef: "lissy93/dashy:latest", Port: 8080, DataMount: "/app/user-data",
}
