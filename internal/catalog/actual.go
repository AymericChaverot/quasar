package catalog

var actual = Template{
	ID: "actual", Name: "Actual Budget", Description: "Envelope budgeting, local first",
	Category: "Utilities", ImageRef: "actualbudget/actual-server:latest", Port: 5006, DataMount: "/data",
}
