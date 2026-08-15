package catalog

var directus = Template{
	ID: "directus", Name: "Directus", Description: "Headless CMS over your own database",
	Category: "Websites", ImageRef: "directus/directus:latest", Port: 8055, DataMount: "/data",
	// Not /directus: that is where the application itself lives, and an
	// empty host directory bound over it hides cli.js, so the container
	// exits before it starts. Database and uploads are pointed into the
	// one directory Quasar does mount.
	Env: "KEY={{RANDOM}}\nSECRET={{RANDOM}}{{RANDOM}}\n" +
		"DB_CLIENT=sqlite3\nDB_FILENAME=/data/directus.db\nSTORAGE_LOCAL_ROOT=/data/uploads\n" +
		"ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD={{RANDOM}}",
}
