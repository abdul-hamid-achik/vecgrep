#!/bin/bash
set -euo pipefail

# Tailwind CSS CLI download script
# Downloads the appropriate Tailwind CSS standalone CLI for the current platform

VERSION="${TAILWIND_VERSION:-v3.4.17}"
BIN_DIR="./bin"

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$OS" in
    darwin)
        case "$ARCH" in
            x86_64) PLATFORM="macos-x64" ;;
            arm64)  PLATFORM="macos-arm64" ;;
            *)      echo "Unsupported architecture: $ARCH"; exit 1 ;;
        esac
        ;;
    linux)
        case "$ARCH" in
            x86_64)  PLATFORM="linux-x64" ;;
            aarch64) PLATFORM="linux-arm64" ;;
            armv7l)  PLATFORM="linux-armv7" ;;
            *)       echo "Unsupported architecture: $ARCH"; exit 1 ;;
        esac
        ;;
    *)
        echo "Unsupported OS: $OS"
        exit 1
        ;;
esac

URL="https://github.com/tailwindlabs/tailwindcss/releases/download/${VERSION}/tailwindcss-${PLATFORM}"
OUTPUT="$BIN_DIR/tailwindcss"

echo "Downloading Tailwind CSS CLI ${VERSION} for ${PLATFORM}..."

# Create bin directory if it doesn't exist
mkdir -p "$BIN_DIR"

# Download
if command -v curl &> /dev/null; then
    curl -sL "$URL" -o "$OUTPUT"
elif command -v wget &> /dev/null; then
    wget -q "$URL" -O "$OUTPUT"
else
    echo "Error: curl or wget required"
    exit 1
fi

# Make executable
chmod +x "$OUTPUT"

echo "Tailwind CSS CLI installed to $OUTPUT"
"$OUTPUT" --help | head -1
