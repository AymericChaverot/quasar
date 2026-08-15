package catalog

var nextcloud = Template{
	ID: "nextcloud", Name: "Nextcloud", Description: "Files, calendar and contacts",
	Category: "Files & sync", ImageRef: "nextcloud:apache", Port: 80, DataMount: "/var/www/html",
	Note: "Ships with SQLite, which is fine for a handful of users. Add Postgres for a bigger install.",
}
