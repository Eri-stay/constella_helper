# syntax=docker/dockerfile:1
# Build context is the repository root:  docker compose -f deploy/docker-compose.yml build telemetry
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY services/telemetry ./services/telemetry
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/telemetry ./services/telemetry/cmd

FROM alpine:3.22
RUN adduser -D -u 10001 app && mkdir -p /data /artifacts /releases && chown app /data /artifacts /releases
USER app
COPY --from=build /out/telemetry /usr/local/bin/telemetry
ENTRYPOINT ["telemetry"]
