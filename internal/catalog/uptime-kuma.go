package catalog

var uptimeKuma = Template{
	ID: "uptime-kuma", Name: "Uptime Kuma", Description: "Uptime monitoring and alerts",
	Category: "Dashboards & monitoring", ImageRef: "louislam/uptime-kuma:1", Port: 3001, DataMount: "/app/data",
}
