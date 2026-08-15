package catalog

var wordpress = Template{
	ID: "wordpress", Name: "WordPress", Description: "The blogging and site platform",
	Category: "Websites", DeployType: "compose", Port: 80, ComposeService: "wordpress",
	Env: "DB_PASSWORD={{RANDOM}}\nDB_ROOT_PASSWORD={{RANDOM}}",
	Compose: `services:
  wordpress:
    image: wordpress:latest
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WORDPRESS_DB_NAME: wordpress
    volumes:
      - ./data/html:/var/www/html
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
      MYSQL_DATABASE: wordpress
      MYSQL_USER: wordpress
      MYSQL_PASSWORD: ${DB_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${DB_ROOT_PASSWORD}
    volumes:
      - ./data/db:/var/lib/mysql
    restart: unless-stopped
`,
}
