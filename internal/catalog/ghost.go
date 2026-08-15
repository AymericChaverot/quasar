package catalog

var ghost = Template{
	ID: "ghost", Name: "Ghost", Description: "Publishing and newsletter platform",
	// A stack rather than a single image: Ghost 5 only supports MySQL in
	// production and, left alone, dials 127.0.0.1:3306 and shuts itself
	// down a second after reporting that the site is available.
	Category: "Websites", DeployType: "compose", Port: 2368, ComposeService: "ghost",
	Env:  "URL={{URL}}\nDB_PASSWORD={{RANDOM}}\nDB_ROOT_PASSWORD={{RANDOM}}",
	Note: "Ghost builds every link from the url in the env below, which was filled in from the subdomain; change it there too if you change the subdomain.",
	Compose: `services:
  ghost:
    image: ghost:5-alpine
    environment:
      url: ${URL}
      database__client: mysql
      database__connection__host: db
      database__connection__user: ghost
      database__connection__password: ${DB_PASSWORD}
      database__connection__database: ghost
    volumes:
      - ./data/content:/var/lib/ghost/content
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: mysql:8
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping -h 127.0.0.1 -uroot -p$$MYSQL_ROOT_PASSWORD"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MYSQL_DATABASE: ghost
      MYSQL_USER: ghost
      MYSQL_PASSWORD: ${DB_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./data/db:/var/lib/mysql
    restart: unless-stopped
`,
}
