package catalog

var matomo = Template{
	ID: "matomo", Name: "Matomo", Description: "Full-featured web analytics",
	Category: "Analytics", DeployType: "compose", Port: 80, ComposeService: "matomo",
	Env: "DB_PASSWORD={{RANDOM}}\nDB_ROOT_PASSWORD={{RANDOM}}",
	Compose: `services:
  matomo:
    image: matomo:apache
    environment:
      MATOMO_DATABASE_HOST: db
      MATOMO_DATABASE_USERNAME: matomo
      MATOMO_DATABASE_PASSWORD: ${DB_PASSWORD}
      MATOMO_DATABASE_DBNAME: matomo
    volumes:
      - ./data/matomo:/var/www/html
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: mariadb:11
    healthcheck:
      test: ["CMD-SHELL", "mariadb-admin ping -h 127.0.0.1 -uroot -p$$MARIADB_ROOT_PASSWORD"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MARIADB_DATABASE: matomo
      MARIADB_USER: matomo
      MARIADB_PASSWORD: ${DB_PASSWORD}
      MARIADB_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./data/db:/var/lib/mysql
    restart: unless-stopped
`,
}
