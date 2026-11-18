# syntax=docker/dockerfile:1
# Build context is the repository root:  docker compose -f deploy/docker-compose.yml build release
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY services/release ./services/release
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/release ./services/release/cmd

FROM alpine:3.22
RUN adduser -D -u 10001 app && mkdir -p /data /artifacts /releases && chown app /data /artifacts /releases
USER app
COPY --from=build /out/release /usr/local/bin/release
ENTRYPOINT ["release"]
