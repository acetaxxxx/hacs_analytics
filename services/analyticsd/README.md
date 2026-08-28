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

`GEMINI_API_KEY` is optional for data collection. When it is absent, the worker
stores a deterministic report with `ai_failed` and a safe error code instead of
silently dropping the report. The API key is never sent to Home Assistant.

The service exposes:

- `GET /api/v1/health/live` for a process liveness probe.
- `GET /api/v1/health` for SQLite/Gemini readiness.
- `POST /api/v1/ingest/events` and `/api/v1/ingest/heartbeat` for the HA integration.
- `POST /api/v1/reports`, then `GET /api/v1/reports/{date}` and `/result`.

Protected endpoints require `Authorization: Bearer $HOMEKEEPER_SHARED_TOKEN`,
`X-Request-ID`, and `X-Homekeeper-Contract-Version: 1`.

## Docker

```sh
docker build -t homekeeper-analyticsd .
docker run --rm -p 8080:8080 \
  -e HOMEKEEPER_SHARED_TOKEN=change-me \
  -e GEMINI_API_KEY=replace-me \
  -e HOMEKEEPER_TIMEZONE=Asia/Taipei \
  -v homekeeper-data:/data \
  homekeeper-analyticsd
```

The container uses a persistent volume for SQLite. Keep the shared token and
Gemini key outside the image, using environment variables or Docker secrets.
