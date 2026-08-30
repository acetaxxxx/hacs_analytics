# Deployment

## Home Assistant / Raspberry Pi

Install the Python custom integration through HACS. Configure the sidecar base URL, shared token, selected report time, entity exclusions, and per-entity profile overrides in the Home Assistant UI. The initial active report time defaults to 08:00 and may be set to 12:00, 18:00, or 22:00. Home Assistant local timezone defines the report window.

The integration does not create an analytics database on the Pi. Its only runtime buffer is the bounded in-memory batch. If the sidecar is unavailable, state events are not durably queued on the Pi; the heartbeat/gap indicator makes this visible. The existing Telegram service is selected in Home Assistant and receives the report after polling completes.

## External Go service

The old Windows computer runs one small Docker container with about 1 GB available RAM and a persistent volume for SQLite. The container exposes the REST port only to the LAN. The Gemini key and shared token are injected as environment variables or Docker secrets.

Every branch push builds and publishes a `linux/amd64` image
`ghcr.io/acetaxxxx/homekeeper-analyticsd:<sha7>`, where `<sha7>` is the first
seven characters of the commit SHA. Pull requests build the image without
publishing it. Pushing a Git tag promotes the image for that commit from its
`<sha7>` tag to the Git tag name; it does not rebuild the image.

Illustrative layout:

```text
hacs_analytics/
  custom_components/home_daily_report/  # HACS-installed Python integration
  services/analyticsd/            # Independent Go HTTP service
  services/analyticsd/internal/   # Go domain, adapters, SQLite, Gemini
  contracts/                      # versioned REST/report schemas
  docs/                           # architecture and operational decisions
  deploy/                         # Docker files and example environment
```

The exact package/module names are implementation details; the two runtimes remain in one monorepo and are released/deployed independently. HACS must package only the custom integration path and not attempt to install the Go container.

## Startup and upgrades

On startup the Go service applies SQLite migrations, validates required secrets, marks expired `ai_running` jobs retryable, then serves health/readiness. A health response must distinguish process liveness from database readiness and Gemini configuration validity.

The integration starts its heartbeat and event batch after the sidecar URL/token configuration is validated. It polls report jobs with bounded backoff. Contract major versions are pinned in both components; upgrade the sidecar and integration in a compatible order documented by the release.

## Resource expectations

The sidecar is optimized for low volume and low memory: one SQLite file, short transactions, bounded report DTOs, and no Prometheus/Timescale dependency in the first release. A later metrics exporter or larger database can be added behind the existing storage/report seams if actual data volume warrants it.

## No backup in first release

Backups are explicitly out of scope. Retention and deletion are still deterministic, but a damaged or deleted SQLite volume is not recoverable by this product. A future backup process can copy the external volume without changing the API contracts.
