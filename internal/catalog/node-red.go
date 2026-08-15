package catalog

var nodeRed = Template{
	ID: "node-red", Name: "Node-RED", Description: "Flow-based event wiring",
	Category: "Automation", ImageRef: "nodered/node-red:latest", Port: 1880, DataMount: "/data",
}
