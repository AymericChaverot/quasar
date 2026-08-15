package catalog

var freshrss = Template{
	ID: "freshrss", Name: "FreshRSS", Description: "Self-hosted feed reader",
	Category: "Reading & RSS", ImageRef: "freshrss/freshrss:latest", Port: 80, DataMount: "/var/www/FreshRSS/data",
	Env: "TZ=Etc/UTC\nCRON_MIN=*/20",
}
