package catalog

var mysql = Template{
	ID: "mysql", Name: "MySQL 8", Description: "Relational database",
	Category: "Databases", ImageRef: "mysql:8", Port: 3306, DataMount: "/var/lib/mysql",
	Env: "MYSQL_ROOT_PASSWORD={{RANDOM}}\nMYSQL_DATABASE=app\nMYSQL_USER=app\nMYSQL_PASSWORD={{RANDOM}}",
	Raw: true,
}
