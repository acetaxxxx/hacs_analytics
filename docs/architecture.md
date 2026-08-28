# Home Assistant Homekeeper architecture

## Purpose

Homekeeper is a read-only household analytics system. It receives selected Home Assistant state data, turns it into bounded statistics and intervals, detects deterministic anomalies and risks, asks Gemini to explain the evidence and identify patterns, and publishes a Traditional Chinese daily report. It never calls a Home Assistant service that changes household state.

The first release is designed for one household:

- Raspberry Pi 4B (4 GB) runs Home Assistant and a thin Python custom integration.
- An older Windows computer runs the external Go service in Docker with a persistent SQLite file.
- Telegram remains a Home Assistant notification concern.
- The Gemini API key is held only by the Go service.

## Runtime topology

```text
Home Assistant (Raspberry Pi)
  homekeeper integration (Python)
    state_changed listener
    30-second / 100-event in-memory batch
    REST client + report polling
    compact sensor + Telegram notification
              |
              | HTTPS or HTTP on trusted LAN, Bearer shared token
              v
External Homekeeper (old Windows / Docker)
  Go HTTP API
    ingest port       report job port       health port
    SQLite repository and rollups
    deterministic rules and profiles
    Gemini client (google.golang.org/genai)
```

The integration is an adapter at the Home Assistant boundary. The Go service owns canonical analytics data and report lifecycle. A sidecar outage may lose the in-memory batch by design, but the next heartbeat lets the sidecar record a `data_gap`; Home Assistant remains responsive.

## Module boundaries

### Python Home Assistant integration

The integration contains only Home Assistant-facing concerns:

- configuration and options flow;
- entity selection and attribute redaction;
- event batching and heartbeat scheduling;
- REST authentication, timeout, and retry behavior;
- report polling and Home Assistant sensor/notification rendering.

It does not own raw analytics storage, profile calculations, anomaly rules, Gemini calls, or the private Recorder database.

### Go external service

The Go service contains:

- HTTP handlers and request validation;
- a storage port and SQLite implementation;
- event normalization, interval closure, snapshots, and daily rollups;
- versioned entity profiles and retained-window recomputation;
- deterministic anomaly/risk rules;
- report orchestration and idempotent job state;
- a Gemini adapter and strict report decoding;
- a bounded report query DTO and report API.

### Seams

The durable seams are:

1. `Home Assistant -> Ingest API`: versioned batch contract.
2. `Report API -> Home Assistant`: versioned report contract.
3. `Report orchestrator -> LLM client`: provider-neutral internal interface, implemented initially only by Gemini.
4. `Analytics domain -> Store`: storage port, implemented initially by SQLite.

These interfaces are intentionally small. Tests use in-memory adapters and a fake LLM client; production adapters are replaceable without changing the domain model.

## Data ownership

| Data | Owner | First-release retention |
| --- | --- | --- |
| HA state and Recorder history | Home Assistant | HA configuration |
| Ingested raw observations/events | Go service SQLite | 30 days |
| Daily rollups and profile version | Go service SQLite | 2 years |
| Report runs and structured reports | Go service SQLite | 2 years |
| Gemini API key | Go service environment/secret | until rotated |

The design intentionally does not migrate the current repository's Store data. The old integration is replaced by the new boundary after the design is approved.

## Resource policy

The Raspberry Pi performs no analytics database writes. It keeps only one bounded in-memory batch, sends at most 100 records per request, and can discard an unacknowledged batch after timeout. The Go service batches SQLite writes in a transaction, uses WAL mode, and prunes by explicit retention jobs. The API never blocks the HA event loop.

## Failure policy

- Invalid batches are rejected with a machine-readable error; they are not partially accepted.
- Duplicate event IDs are harmless and return an idempotent ingest result.
- A missing heartbeat is represented as `data_gap`, not inferred as a healthy interval.
- A report date has one idempotency key, so schedule, manual retry, and polling cannot create competing reports.
- Gemini transient failure is retried at 15 minutes, 1 hour, 4 hours, and next day. After the final attempt the report is `ai_failed` and Telegram is notified.
- Gemini authentication, invalid request, or schema failure is not retried using the transient schedule.
- A report can be deterministic-only only when explicitly configured as a future policy; the initial agreed behavior retries later when Gemini is unavailable.
