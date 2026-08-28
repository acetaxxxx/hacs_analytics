# Homekeeper analytics sidecar

This directory is an independently deployed Go service. HACS installs only
the Home Assistant integration under `custom_components/home_daily_report`;
it does not build or supervise this service.

## Local run

The service requires `HOMEKEEPER_SHARED_TOKEN` and stores SQLite data at
`HOMEKEEPER_DB_PATH` (default `./data/homekeeper.db`). It listens on `:8080`
by default.

```sh
HOMEKEEPER_SHARED_TOKEN=change-me go run ./cmd/analyticsd
curl http://localhost:8080/api/v1/health
```

`GEMINI_API_KEY` is optional for this foundation release. The health response
reports whether it is configured, while data collection remains ready without
it. Ingest and report endpoints are reserved for later tickets.

## Docker

```sh
docker build -t homekeeper-analyticsd .
docker run --rm -p 8080:8080 \
  -e HOMEKEEPER_SHARED_TOKEN=change-me \
  -v homekeeper-data:/data \
  homekeeper-analyticsd
```

The container uses a persistent volume for SQLite. Keep the shared token and
Gemini key outside the image, using environment variables or Docker secrets.
