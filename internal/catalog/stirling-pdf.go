package catalog

var stirlingPdf = Template{
	ID: "stirling-pdf", Name: "Stirling PDF", Description: "Split, merge and convert PDFs",
	Category: "Utilities", ImageRef: "ghcr.io/stirling-tools/stirling-pdf:latest", Port: 8080, DataMount: "/configs",
}
