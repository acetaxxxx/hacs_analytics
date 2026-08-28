# Gemini integration

The first release uses the official Google GenAI Go SDK directly: `google.golang.org/genai`. Since the product currently targets Gemini only, a provider gateway or multi-provider abstraction would add a second configuration surface without giving the household a benefit. The domain still depends on a small internal `LLMClient` interface so the Gemini implementation can be faked in tests and replaced later.

## Configuration

The API key is read only by the external Go service from an environment variable or Docker secret. It is never returned by the API, written to Home Assistant options, or included in logs. The model is configurable through the Home Assistant UI and sent as an allowlisted model string to the sidecar; default is a Gemini Flash model. Pro and other supported Gemini models may be selected.

The backend is the Gemini Developer API in the first release. Vertex AI/enterprise credentials are deferred. The SDK version is pinned, with the initial dependency constrained below `2.0.0` until the next major version is qualified.

## Call contract

```text
Generate(ctx, PromptEnvelope, ReportSchema) -> ReportObject, Usage, Error
```

`PromptEnvelope` contains the report date, timezone, deterministic facts, bounded aggregates, profile descriptions, data-quality limitations, and evidence IDs. It does not contain a raw event dump or unredacted HA attributes. The system instruction requires Traditional Chinese prose, fixed English JSON keys/enums, evidence-grounded claims, explicit uncertainty, and no automation commands.

The request asks Gemini for structured JSON using the report schema. The Go service validates the decoded response against the same schema, checks enum values and evidence references, and rejects unknown unsafe instructions. A provider response that cannot be validated is an `ai_schema_invalid` attempt.

## Reliability

The report run stores provider, model, request ID, attempt, timing, usage if returned, input hash, and error code in SQLite. Transient failures (timeout, connection reset, HTTP 429/5xx) are retried at 15 minutes, 1 hour, 4 hours, and next day. Authentication, invalid request, quota configuration, and schema failures are surfaced without an unbounded retry loop.

The daily report is idempotent by report date. A retry may repeat an external API request, but it cannot create a second stored completed report for the same date. A configurable future budget can cap attempts or estimated tokens; the first release does not semantically cap anomaly/suggestion count, but it does enforce provider context/body limits and records any truncation.

## Testing

Unit tests inject a fake `LLMClient` with valid, invalid, timeout, and rate-limit responses. Contract fixtures cover every report section and redaction case. Live Gemini tests are manual/opt-in and use a real key outside normal CI.
