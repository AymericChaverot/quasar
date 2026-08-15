package catalog

var trilium = Template{
	ID: "trilium", Name: "Trilium Notes", Description: "Hierarchical personal knowledge base",
	Category: "Notes & docs", ImageRef: "triliumnext/notes:latest", Port: 8080, DataMount: "/home/node/trilium-data",
}
