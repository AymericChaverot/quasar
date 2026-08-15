package catalog

var n8n = Template{
	ID: "n8n", Name: "n8n", Description: "Workflow automation",
	Category: "Automation", ImageRef: "n8nio/n8n:latest", Port: 5678, DataMount: "/home/node/.n8n",
	Env:  "N8N_HOST={{HOST}}\nWEBHOOK_URL={{URL}}/\nN8N_PROXY_HOPS=1",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
}
