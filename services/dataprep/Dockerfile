# syntax=docker/dockerfile:1
# Build context is the repository root:  docker compose -f deploy/docker-compose.yml build dataprep
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY internal ./internal
COPY services/dataprep ./services/dataprep
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dataprep ./services/dataprep/cmd

FROM alpine:3.22
RUN adduser -D -u 10001 app && mkdir -p /data /artifacts && chown app /data /artifacts
USER app
COPY --from=build /out/dataprep /usr/local/bin/dataprep
ENTRYPOINT ["dataprep"]
