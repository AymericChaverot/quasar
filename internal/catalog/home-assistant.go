package catalog

var homeAssistant = Template{
	ID: "home-assistant", Name: "Home Assistant", Description: "Home automation hub",
	Category: "Automation", ImageRef: "ghcr.io/home-assistant/home-assistant:stable", Port: 8123, DataMount: "/config",
	Note: "Runs on the bridge network here, so devices found by broadcast discovery will not appear; add them by IP.",
}
