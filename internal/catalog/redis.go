package catalog

var redis = Template{
	ID: "redis", Name: "Redis 7", Description: "In-memory data store",
	Category: "Databases", ImageRef: "redis:7-alpine", Port: 6379, DataMount: "/data",
	Raw: true,
}
