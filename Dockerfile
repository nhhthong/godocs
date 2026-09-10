# syntax=docker/dockerfile:1
# Multi-stage build: the "build" stage provides the complete Go toolchain, while the final
# runtime stage contains solely a static binary—yielding a minimal image (~10MB) and a minimized attack surface.

# ---- Build Stage ----
FROM golang:1.23-alpine AS build
WORKDIR /src

# Copy dependency manifests ahead of source files to leverage Docker layer caching
# when dependencies remain unchanged across code edits.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
# CGO_ENABLED=0    -> Compiles a pure static binary with zero libc dependencies, ideal for distroless
# -trimpath        -> Strips absolute host filesystem paths from binary metadata to ensure reproducible builds
# -ldflags="-s -w" -> Strips symbol tables and DWARF debug information to minimize binary footprint
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---- Runtime Stage ----
# distroless/static contains no shell, package manager, or libc runtime;
# it provides only what is needed to execute a static binary under an unprivileged "nonroot" user (UID 65532).
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/api /app/api
COPY migrations /app/migrations
COPY web /app/web
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/api"]
