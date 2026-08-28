# REST contract

The OpenAPI source of truth is [`contracts/openapi.yaml`](../contracts/openapi.yaml). All timestamps are RFC 3339 UTC strings unless a field explicitly says local date/time. All requests include `Authorization: Bearer <shared-token>` and a unique `X-Request-ID`.

All protected endpoints, including report polling, require both `X-Request-ID`
and `X-Homekeeper-Contract-Version: 1`. Health endpoints are public and are
intended for container probes.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/ingest/events` | Ingest an event/snapshot batch idempotently |
| `POST` | `/api/v1/ingest/heartbeat` | Close heartbeat interval and detect gaps |
| `POST` | `/api/v1/reports` | Request a report for a local date |
| `GET` | `/api/v1/reports/{report_date}` | Poll report job status |
| `GET` | `/api/v1/reports/{report_date}/result` | Fetch complete report JSON |
| `GET` | `/api/v1/health` | Liveness/readiness without analytics data |

## Ingest behavior

The event batch has a maximum of 100 records. The sidecar validates every record before writing any record. A repeated `event_id` is classified as `duplicate` and does not alter aggregation. A successful response identifies `accepted`, `duplicates`, and `ignored` counts. Validation errors use a stable `code` and do not leak SQLite errors.

The heartbeat body identifies the sender instance and its observed time. A heartbeat does not imply that no state event occurred; it only establishes liveness. The sidecar records a `data_gap` when the elapsed time since the previous heartbeat exceeds the configured tolerance.

## Report behavior

`POST /reports` requires a local `report_date`. If a report job for that date exists, the response returns the existing job and does not start a duplicate Gemini call. A future force/recompute endpoint must be explicit and separately authorized; it is not part of the first release.

The result endpoint returns the versioned report object, including `ai_status`, `data_quality`, and evidence references. The integration is responsible for deciding how to display it, not for reimplementing report semantics.

## Error semantics

| HTTP | Meaning | Retry by HA integration |
| --- | --- | --- |
| `400` | Invalid schema/date/record | No |
| `401` | Missing or wrong shared token | No; notify configuration problem |
| `404` | Unknown report date/result | No |
| `409` | Conflicting request or stale contract version | No; surface incompatibility |
| `413` | Body/batch too large | No; split/reduce batch |
| `429` | Sidecar backpressure | Yes, bounded retry |
| `500` | Sidecar internal error | Yes, bounded retry |
| `503` | Sidecar not ready | Yes, bounded retry |

## Compatibility

The major API version is in the path. The integration sends `X-Homekeeper-Contract-Version`; the sidecar rejects unsupported major versions. New optional fields are backward-compatible. Removing or changing field meaning requires a new major version and a migration note.
