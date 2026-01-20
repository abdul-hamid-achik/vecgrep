# syntax=docker/dockerfile:1

# =============================================================================
# Build stage: Generate templates and build the Go binary
# =============================================================================
FROM golang:1.25-bookworm AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Install templ for template generation
RUN go install github.com/a-h/templ/cmd/templ@latest

WORKDIR /app

# Copy go.mod and go.sum first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Generate templ files
RUN templ generate

# Build the binary with CGO enabled (required for sqlite-vec)
ENV CGO_ENABLED=1
RUN go build -ldflags="-s -w" -o /app/bin/vecgrep ./cmd/vecgrep

# =============================================================================
# Frontend build stage: Build Tailwind CSS
# =============================================================================
FROM node:20-slim AS frontend

WORKDIR /app

# Install tailwindcss
RUN npm install -g tailwindcss

# Copy CSS source
COPY assets/css/input.css ./assets/css/
COPY tailwind.config.js ./

# Generate CSS output
RUN npx tailwindcss -i ./assets/css/input.css -o ./assets/css/output.css --minify

# =============================================================================
# Final stage: Runtime image
# =============================================================================
FROM debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd -r -u 1000 -m vecgrep

# Create necessary directories
RUN mkdir -p /data /app && chown -R vecgrep:vecgrep /data /app

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/bin/vecgrep /usr/local/bin/vecgrep

# Copy generated CSS
COPY --from=frontend /app/assets/css/output.css /app/internal/web/static/css/

# Set ownership
RUN chown -R vecgrep:vecgrep /app

# Switch to non-root user
USER vecgrep

# Environment variables
ENV VECGREP_DATA_DIR=/data
ENV VECGREP_EMBEDDING_OLLAMA_URL=http://ollama:11434

# Expose web server port
EXPOSE 8080

# Volume for persistent data
VOLUME ["/data"]

# Default command
ENTRYPOINT ["vecgrep"]
CMD ["serve", "--web", "--port", "8080"]
