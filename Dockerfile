# --- Build stage -------------------------------------------------------------
FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X quasar/internal/version.Version=${VERSION}" \
    -o /quasar ./cmd/server

# --- Runtime stage -----------------------------------------------------------
# git is needed for "Git build" deploys; docker-cli + compose plugin for
# "Docker Compose" deploys (both talk to the socket proxy via DOCKER_HOST).
FROM alpine:3.20
RUN apk add --no-cache ca-certificates git docker-cli docker-cli-compose
COPY --from=build /quasar /usr/local/bin/quasar
EXPOSE 8080
ENTRYPOINT ["quasar"]
