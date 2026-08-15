package catalog

var postgres = Template{
	ID: "postgres", Name: "PostgreSQL 17", Description: "Relational database",
	Category: "Databases", ImageRef: "postgres:17-alpine", Port: 5432, DataMount: "/var/lib/postgresql/data",
	Env: "POSTGRES_USER=app\nPOSTGRES_PASSWORD={{RANDOM}}\nPOSTGRES_DB=app",
	Raw: true,
}
