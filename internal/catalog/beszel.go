package catalog

var beszel = Template{
	ID: "beszel", Name: "Beszel", Description: "Lightweight server monitoring",
	Category: "Dashboards & monitoring", ImageRef: "henrygd/beszel:latest", Port: 8090, DataMount: "/beszel_data",
}
