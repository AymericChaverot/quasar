package catalog

var mongo = Template{
	ID: "mongo", Name: "MongoDB 7", Description: "Document database",
	Category: "Databases", ImageRef: "mongo:7", Port: 27017, DataMount: "/data/db",
	Env: "MONGO_INITDB_ROOT_USERNAME=root\nMONGO_INITDB_ROOT_PASSWORD={{RANDOM}}",
	Raw: true,
}
