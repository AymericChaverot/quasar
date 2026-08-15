package catalog

var memos = Template{
	ID: "memos", Name: "Memos", Description: "Lightweight notes, one thought per card",
	Category: "Notes & docs", ImageRef: "neosmemo/memos:stable", Port: 5230, DataMount: "/var/opt/memos",
}
