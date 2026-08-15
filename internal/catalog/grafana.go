package catalog

var grafana = Template{
	ID: "grafana", Name: "Grafana", Description: "Metrics dashboards",
	Category: "Dashboards & monitoring", ImageRef: "grafana/grafana:latest", Port: 3000, DataMount: "/var/lib/grafana",
	Env: "GF_SECURITY_ADMIN_PASSWORD={{RANDOM}}",
}
