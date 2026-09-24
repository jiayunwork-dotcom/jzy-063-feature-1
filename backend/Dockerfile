# Build context MUST be the repository root (see docker-compose.yml).
FROM golang:1.22-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/config-server ./cmd/server

# Minimal runtime image; schema files are applied by the server on startup.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/config-server /app/config-server
COPY deploy/db /app/db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/config-server"]
