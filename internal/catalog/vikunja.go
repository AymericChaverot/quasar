package catalog

var vikunja = Template{
	ID: "vikunja", Name: "Vikunja", Description: "To-do lists and project planning",
	Category: "Tasks & projects", ImageRef: "vikunja/vikunja:latest", Port: 3456, DataMount: "/data",
	// Vikunja keeps its database and its uploads in two different places by
	// default — /db and /app/vikunja/files — and Quasar binds one directory
	// per app. Both are pointed inside it, or the container exits on the
	// first migration because /db does not exist.
	Env: "VIKUNJA_SERVICE_PUBLICURL={{URL}}\nVIKUNJA_SERVICE_SECRET={{RANDOM}}{{RANDOM}}\n" +
		"VIKUNJA_DATABASE_PATH=/data/vikunja.db\nVIKUNJA_FILES_BASEPATH=/data/files",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
}
