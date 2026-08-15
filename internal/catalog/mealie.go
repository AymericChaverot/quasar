package catalog

var mealie = Template{
	ID: "mealie", Name: "Mealie", Description: "Recipe manager and meal planner",
	Category: "Utilities", ImageRef: "ghcr.io/mealie-recipes/mealie:latest", Port: 9000, DataMount: "/app/data",
	Env: "ALLOW_SIGNUP=false\nBASE_URL={{URL}}",
}
