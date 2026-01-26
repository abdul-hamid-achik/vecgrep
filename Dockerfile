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
# Stage 2: Build Go binary (Alpine - no CGO needed)
# =============================================================================
FROM golang:1.25-alpine AS builder

# Build args for version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk add --no-cache git

RUN go install github.com/a-h/templ/cmd/templ@latest

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
COPY --from=tailwind /build/output.css ./internal/web/static/css/output.css

RUN templ generate

ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w \
    -X github.com/abdul-hamid-achik/vecgrep/internal/version.Version=${VERSION} \
    -X github.com/abdul-hamid-achik/vecgrep/internal/version.Commit=${COMMIT} \
    -X github.com/abdul-hamid-achik/vecgrep/internal/version.Date=${BUILD_DATE}" \
    -o vecgrep ./cmd/vecgrep

# =============================================================================
# Stage 3: Runtime (Alpine for minimal size)
# =============================================================================
FROM alpine:3.21

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 1000 vecgrep \
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
