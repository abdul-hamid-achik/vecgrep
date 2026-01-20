#!/bin/sh
set -e

# Initialize vecgrep if not already initialized
if [ ! -d "/data/.vecgrep" ]; then
    echo "Initializing vecgrep..."
    cd /data && vecgrep init
fi

# Execute the command
exec "$@"
