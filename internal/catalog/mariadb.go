package catalog

var mariadb = Template{
	ID: "mariadb", Name: "MariaDB 11", Description: "Relational database",
	Category: "Databases", ImageRef: "mariadb:11", Port: 3306, DataMount: "/var/lib/mysql",
	Env: "MARIADB_ROOT_PASSWORD={{RANDOM}}\nMARIADB_DATABASE=app\nMARIADB_USER=app\nMARIADB_PASSWORD={{RANDOM}}",
	Raw: true,
}
