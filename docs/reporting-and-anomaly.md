# Reporting and anomaly model

## Deterministic facts first

The Go service produces a deterministic evidence set before invoking Gemini. It includes:

- state and availability durations;
- numeric distribution, slope, deltas, and coverage;
- profile thresholds and fixed safety rules;
- baseline comparisons after 14 days of data;
- heartbeat/data-gap facts;
- evidence IDs that point to bounded rollup or interval records.

Gemini is an explanation and pattern-finding layer. It cannot invent a risk without evidence and cannot issue an automation command.

## Risk levels

| Level | Initial examples | Delivery |
| --- | --- | --- |
| `high` | smoke/gas/leak signal, security breach-like state, severe temperature, critical unavailable sensor | immediate report/Telegram, subject to cooldown |
| `medium` | unusual energy, long unavailable interval, repeated contact activity, sustained comfort deviation | daily report, cooldown |
| `low` | weak pattern, gradual drift, optimization opportunity | daily report |

Rules are profile-aware. For example, a battery percentage can use a battery threshold, while a generic numeric entity cannot be called a battery without metadata. Thresholds and cooldowns are configuration data, not constants hidden in the report renderer.

## Cold start and coverage

The baseline period is 14 days. During cold start, fixed safety rules still run, while statistical claims are marked `insufficient_baseline`. A data gap, low sample coverage, or long unavailable period lowers confidence and is surfaced in `data_quality` rather than silently treated as normal.

## Report shape

The report uses stable English keys and enum values; human-facing prose is Traditional Chinese:

```json
{
  "schema_version": 1,
  "report_date": "2026-08-27",
  "window": {"start": "...Z", "end": "...Z", "timezone": "Asia/Taipei"},
  "summary": "今天家中整體狀況…",
  "data_quality": {"coverage": 0.98, "data_gaps": [], "limitations": []},
  "anomalies": [],
  "risks": [],
  "patterns": [],
  "suggestions": [],
  "confidence": "medium",
  "evidence": [],
  "ai_status": {"status": "completed", "provider": "gemini", "model": "..."}
}
```

An anomaly/risk item contains a stable ID, level, title, Traditional Chinese explanation, entity pseudonyms, evidence IDs, confidence, and whether it was deterministic or AI-enriched. Suggestions must state the observed evidence and a non-actionable recommendation. The report renderer does not execute the suggestion.

## Gemini failure

The report remains in `ai_pending`/`ai_retry_scheduled` after a transient Gemini failure. Retries occur at 15 minutes, 1 hour, 4 hours, and the next day. After the final attempt, status becomes `ai_failed` and Telegram reports the failure. Authentication errors, invalid requests, and schema violations are recorded and not blindly retried.

## Notification

Home Assistant polls the report and uses its already configured Telegram notification service. The sidecar does not need Telegram credentials. Long messages are split at safe boundaries, and the full JSON remains available through the report endpoint.
