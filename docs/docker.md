# Docker

The container runs vecgrep from `/workspace` and stores global vecgrep state under `/home/vecgrep/.vecgrep`.

## Compose

```bash
docker compose up -d
```

The Compose setup mounts:

- the current repository at `/workspace`
- a persistent `vecgrep-home` volume at `/home/vecgrep/.vecgrep`

## Run Commands

```bash
docker compose run --rm vecgrep init
docker compose run --rm vecgrep index
docker compose run --rm vecgrep search "configuration loading"
```

## Direct Docker

```bash
docker build -t vecgrep:latest .
docker run --rm -v "$(pwd):/workspace" vecgrep:latest search "error handling"
```

## Ollama

The default provider expects Ollama to be reachable from the container. On macOS, you can run Ollama on the host:

```bash
OLLAMA_METAL=1 OLLAMA_HOST=0.0.0.0 ollama serve
```

Then configure the container to use the host address as needed for your environment.
