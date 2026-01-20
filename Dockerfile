# syntax=docker/dockerfile:1

# =============================================================================
# Stage 1: Build Tailwind CSS
# =============================================================================
FROM node:22-alpine3.21 AS tailwind

WORKDIR /build

RUN npm init -y && npm install tailwindcss @tailwindcss/cli --save-dev

COPY assets/css/input.css ./assets/css/
COPY internal/web/templates ./internal/web/templates/

RUN npx @tailwindcss/cli -i ./assets/css/input.css -o ./output.css --minify

# =============================================================================
# Stage 2: Build Go binary (Debian for sqlite-vec glibc compatibility)
# =============================================================================
FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libc6-dev libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*

RUN go install github.com/a-h/templ/cmd/templ@latest

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
COPY --from=tailwind /build/output.css ./internal/web/static/css/output.css

RUN templ generate

ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags="-s -w" -o vecgrep ./cmd/vecgrep

# =============================================================================
# Stage 3: Runtime (Alpine for minimal size)
# =============================================================================
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 1000 -m vecgrep \
    && mkdir -p /data \
    && chown vecgrep:vecgrep /data

COPY --from=builder /build/vecgrep /usr/local/bin/
COPY docker-entrypoint.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

USER vecgrep
WORKDIR /data

ENV VECGREP_DATA_DIR=/data
ENV VECGREP_OLLAMA_URL=http://host.docker.internal:11434

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["vecgrep", "serve", "--web", "--host", "0.0.0.0", "--port", "8080"]
