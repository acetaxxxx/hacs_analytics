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
  -v ./data:/data \
  homekeeper-analyticsd
```

The SQLite database is visible on the host at `./data/homekeeper.db`. Keep the
shared token and Gemini key outside the image, using environment variables or
Docker secrets.

## Docker Compose

From the repository root, copy `.env.example` to `.env`, set the published
`HOMEKEEPER_IMAGE_TAG` and `HOMEKEEPER_SHARED_TOKEN`, and optionally set
`GEMINI_API_KEY`:

```sh
cp .env.example .env
docker compose up -d
curl http://localhost:8080/api/v1/health/live
```

The Compose service always pulls the image from
`ghcr.io/acetaxxxx/homekeeper-analyticsd:<tag>`, persists SQLite data in the
host directory `${HOMEKEEPER_DATA_DIR:-./data}`, and restarts automatically
unless stopped. Set `HOMEKEEPER_DATA_DIR` in `.env` to an absolute directory
such as `C:/homekeeper/data`. CI publishes seven-character commit tags for
branch pushes and release tags for versioned releases.

If you previously used the named `homekeeper-data` volume, switching to this
bind mount does not copy its contents; the new host directory starts with a
new database unless you migrate the file yourself.

## CI images

Every branch push builds and publishes an image to
GHCR for `linux/amd64`, using the first seven characters of the commit SHA:

```text
ghcr.io/acetaxxxx/homekeeper-analyticsd:<sha7>
```

Pull requests build the image for verification but do not publish it. When a
Git tag is pushed, the release workflow pulls the image for that commit's
`<sha7>` tag and publishes the same image under the sanitized Git tag name.
