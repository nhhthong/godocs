# syntax=docker/dockerfile:1
# Multi-stage build: the "build" stage has the full Go toolchain; the final stage
# is a distroless static image (~10MB) with no shell, no package manager, running
# as a non-root user.

# ---- Build Stage ----
FROM golang:1.23-alpine AS build
WORKDIR /src

# Dependencies first so `docker build` caches them across source-only changes.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
# CGO_ENABLED=0 -> static binary, no libc dependency, works on distroless
# -trimpath / -ldflags="-s -w" -> reproducible build, smaller binary
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# The final stage's USER can't run `mkdir`/`chown` (no shell in distroless), so the
# writable data directory is created and owned here, then copied over with --chown.
RUN mkdir -p /out/data

# ---- Runtime Stage ----
FROM gcr.io/distroless/static-debian12

# /app: the binary and its read-only assets (owned by root, never written to).
WORKDIR /app
COPY --from=build /out/api /app/api
COPY migrations /app/migrations
COPY web /app/web

# /data: the only directory the process writes to (SQLite file, uploads, logs).
# 65532:65532 is distroless's built-in "nonroot" user/group.
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data

ENV APP_ADDR=:8080 \
    APP_DSN="file:/data/godocs.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)" \
    APP_STORAGE=/data/uploads \
    APP_LOG_DIR=/data/log

EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app/api"]
