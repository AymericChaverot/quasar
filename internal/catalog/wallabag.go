package catalog

var wallabag = Template{
	ID: "wallabag", Name: "Wallabag", Description: "Read-it-later article archive",
	Category: "Reading & RSS", ImageRef: "wallabag/wallabag:latest", Port: 80, DataMount: "/var/www/wallabag/data",
	Env:  "SYMFONY__ENV__DOMAIN_NAME={{URL}}\nSYMFONY__ENV__SECRET={{RANDOM}}{{RANDOM}}",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
}
