# Homekeeper analytics system rewrite

Status: ready-for-agent

## Problem Statement

The current Home Assistant integration is a useful MVP, but it keeps analytics state in Home Assistant's local Store and derives statistics from event observations. This creates unnecessary Raspberry Pi SD-card writes, makes long-term querying and recomputation difficult, and causes report quality to depend on process uptime and sampling frequency.

The household needs a read-only housekeeper that can summarize Home Assistant every day, identify abnormal or risky conditions, find recurring patterns, and suggest improvements. It must handle different kinds of entities differently, keep uncertainty visible, protect private household data, and continue operating sensibly when the external service or Gemini is unavailable.

The first product is for one household. It does not need to preserve or migrate the old Store data, provide backups, support multiple LLM providers, or execute Home Assistant actions.

## Solution

Replace the current implementation with two independently deployed runtimes in one monorepo:

1. A thin Python Home Assistant custom integration on the Raspberry Pi. It collects state changes and numeric snapshots, applies entity selection and privacy rules, batches data in memory, sends it over a versioned REST contract, polls report jobs, and renders the result through Home Assistant and the existing Telegram integration.
2. A small Go analytics sidecar on the older Windows computer, running in Docker with about 1 GB of available RAM. It owns an external SQLite database, profile-aware aggregation, deterministic anomaly/risk detection, report orchestration, and the Gemini integration.

The primary product seam is the authenticated versioned REST API between the two runtimes. The sidecar is the canonical owner of analytics data. The first release uses HA-to-sidecar push and does not have the sidecar pull Home Assistant through REST or WebSocket.

The sidecar stores raw records for 30 days and daily rollups, report runs, and LLM attempt metadata for 2 years. A profile change recomputes only retained raw data and leaves older rollups associated with their historical profile version.

Deterministic rules produce evidence first. Gemini receives a bounded, redacted aggregate DTO and returns a schema-validated Traditional Chinese report with fixed English keys and enum values. The API key exists only in the sidecar environment or Docker secret. Gemini is initially accessed directly through Google's official Go SDK, `google.golang.org/genai`; the model is configurable from Home Assistant, defaults to Flash, and may be any supported Gemini model including Pro.

The report covers the previous complete local calendar day at one selected active time, defaulting to 08:00 with 12:00, 18:00, and 22:00 as alternatives. Scheduled and manual requests are idempotent by report date. Transient Gemini errors retry at 15 minutes, 1 hour, 4 hours, and the next day, then become `ai_failed` and notify the user.

## User Stories

1. As a household owner, I want a daily Traditional Chinese summary of my Home Assistant data, so that I can understand the overall state of my home without reading individual sensors.
2. As a household owner, I want the housekeeper to identify anomalies, risks, recurring patterns, and possible improvements, so that I can notice issues that are difficult to see manually.
3. As a household owner, I want the housekeeper to be read-only, so that an AI suggestion cannot directly turn devices on, unlock a door, or change an automation.
4. As a household owner, I want to choose the daily report time from 08:00, 12:00, 18:00, or 22:00, so that the report arrives at a useful time.
5. As a household owner, I want the report to use Home Assistant's configured local timezone, so that a report for a local date has intuitive day boundaries.
6. As a household owner, I want the daily report to cover the previous complete local calendar day, so that the report does not mix a partial current day with historical data.
7. As a household owner, I want to request a report for a specified ISO local date, so that I can inspect a particular day manually.
8. As a household owner, I want repeated scheduled and manual requests for one date to reuse the same report job, so that retries do not create duplicate reports or duplicate Gemini charges.
9. As a household owner, I want the full structured report to remain available through the integration, so that a compact notification does not hide details.
10. As a household owner, I want the report sent through my existing Home Assistant Telegram setup, so that I do not have to configure a second notification system.
11. As a household owner, I want long Telegram reports split safely, so that a complete report is delivered even when it exceeds one message's size limit.
12. As a household owner, I want all entities selected by default, so that a newly installed household starts collecting useful data without manually selecting every entity.
13. As a household owner, I want to exclude noisy, private, or irrelevant entities, so that the data volume and privacy exposure stay under my control.
14. As a household owner, I want an explicit exclusion to win over every automatic profile, so that a sensitive entity can never be collected accidentally.
15. As a household owner, I want automatic profiles based on domain and device class, so that common entity types receive useful rules without manual setup.
16. As a household owner, I want to override the profile for an individual entity, so that two entities of the same broad type can have different thresholds or sampling needs.
17. As a household owner, I want unknown entities to use a safe generic profile, so that unsupported entities still contribute basic state and availability facts without invented numeric meaning.
18. As a household owner, I want numeric entities to receive state-change data and periodic snapshots, so that slow-changing values have better time coverage than event-only sampling.
19. As a household owner, I want to adjust the snapshot interval per entity, so that important or high-frequency sensors can use a different collection cost.
20. As a household owner, I want state duration measured for normal, unknown, and unavailable states, so that a sensor that silently stops reporting is distinguishable from a stable sensor.
21. As a household owner, I want energy, climate, battery, contact/security, light/media, binary, numeric, and generic entities to have different analysis rules, so that their values are interpreted according to their meaning.
22. As a household owner, I want extra attributes disabled by default and opt-in per entity, so that useful metadata is available without sending every private attribute.
23. As a household owner, I want tokens, credentials, addresses, free text, camera/media content, and other sensitive values removed before persistence and before Gemini, so that external AI processing has a minimized privacy footprint.
24. As a household owner, I want stable pseudonymous entity IDs and safe labels in the AI input, so that the report can refer to evidence consistently without exposing unnecessary names.
25. As a household owner, I want deterministic safety rules to run even during the first 14 days, so that a smoke, gas, leak, security, or severe-temperature problem is not hidden while a statistical baseline is being built.
26. As a household owner, I want statistical findings marked as insufficient when the baseline or coverage is inadequate, so that the housekeeper does not present weak evidence as certainty.
27. As a household owner, I want high-risk findings surfaced immediately subject to cooldown, so that urgent household conditions are not delayed until the next daily summary.
28. As a household owner, I want medium- and low-risk findings included in the daily report, so that useful observations do not create excessive immediate notifications.
29. As a household owner, I want cooldowns for repeated risks, so that one continuing issue does not flood Telegram with duplicate messages.
30. As a household owner, I want each finding to show its evidence and confidence, so that I can verify why the housekeeper reached its conclusion.
31. As a household owner, I want suggestions to be advisory and evidence-based, so that I can decide whether an improvement is appropriate for my home.
32. As a household owner, I want Gemini to produce fixed structured JSON, so that Home Assistant can render reports reliably instead of parsing fragile prose.
33. As a household owner, I want Gemini prose in Traditional Chinese while machine keys remain stable English names, so that the report is natural to read and stable to integrate.
34. As a household owner, I want transient Gemini failures retried later at defined times, so that a temporary provider outage does not permanently lose the daily report.
35. As a household owner, I want a final `ai_failed` status and notification after all retries are exhausted, so that I know a report is incomplete rather than mistaking silence for success.
36. As a household owner, I want the sidecar to remain usable when Gemini is not configured or unavailable, so that data collection and deterministic evidence are not coupled to an external API's uptime.
37. As a household owner, I want the Raspberry Pi to avoid writing analytics data to its SD card, so that Home Assistant storage wear and resource contention are reduced.
38. As a household owner, I want the sidecar to accept batches of state data, so that network overhead is bounded and Home Assistant does not perform one request per state event.
39. As a household owner, I want a heartbeat recorded about every minute, so that the report can distinguish no household activity from a broken connection.
40. As a household owner, I want missing heartbeats represented as `data_gap`, so that incomplete collection lowers report confidence instead of being interpreted as normal behavior.
41. As a household owner, I want duplicate event IDs to be harmless, so that a retry after a network timeout does not double-count transitions or measurements.
42. As a household owner, I want sidecar downtime to leave Home Assistant loaded and responsive, so that an analytics outage does not disrupt household control.
43. As a household owner, I want the integration to close HTTP resources and cancel reconnect tasks on reload or unload, so that configuration changes and HA restarts do not leak connections or background work.
44. As a household owner, I want the external database retention to be explicit, so that disk usage remains predictable on the old Windows computer.
45. As a household owner, I want old data discarded rather than migrated from the current MVP, so that the rewrite can use a clean schema without carrying incompatible assumptions.
46. As a maintainer, I want the HA integration and Go sidecar in one monorepo, so that contracts, documentation, and release changes can be reviewed together.
47. As a maintainer, I want HACS to package only the custom integration, so that HACS installation does not pretend to install or supervise an unrelated Go process.
48. As a maintainer, I want the Go service to have its own module, container, build, and release process, so that it can be deployed on the older Windows host independently.
49. As a maintainer, I want a small storage interface and a small LLM client interface, so that domain tests can use in-memory and fake adapters without requiring a database or API key.
50. As a maintainer, I want contract versions and migration versions recorded, so that an integration/sidecar mismatch fails clearly instead of corrupting data.
51. As a maintainer, I want request, report, and LLM attempt metadata persisted, so that retries, failures, cost/usage, and report provenance can be diagnosed.
52. As a maintainer, I want the implementation tested through external behavior and the primary REST seam, so that refactoring internal modules does not require rewriting implementation-coupled tests.

## Implementation Decisions

### Product and deployment

- The product is a single-household, read-only Home Assistant housekeeper.
- The runtime consists of a Python custom integration and an external Go sidecar in one monorepo, deployed independently.
- Home Assistant runs on the Raspberry Pi; the Go sidecar runs in Docker on the older Windows computer with approximately 1 GB RAM.
- HACS manages only the Home Assistant integration. The sidecar has an independent Go module, Docker/build artifacts, installation instructions, health behavior, and release version.
- The first release has no old-data migration, no backup workflow, no Prometheus/Timescale dependency, and no requirement to install the sidecar through HACS.

### Primary seam and module structure

- The primary product seam is the authenticated, versioned HA-to-sidecar REST API.
- The integration module owns HA lifecycle, config/options flow, entity selection, safe metadata extraction, in-memory batching, heartbeat scheduling, REST client behavior, report polling, sensor rendering, and Telegram delivery.
- The sidecar HTTP module owns authentication, request IDs, contract-version checks, body limits, validation, and HTTP error mapping.
- The sidecar domain module owns normalized observations, snapshots, transitions, state intervals, heartbeat gaps, profile selection, daily rollups, deterministic rules, report evidence, and report state transitions.
- The sidecar storage module depends on a small storage port. SQLite is the first production adapter; in-memory storage is the test adapter.
- The report module depends on a small `LLMClient` port. Gemini is the first production adapter; a fake client is the test adapter.
- Tests should exercise the REST seam at the highest useful level. Storage and LLM ports are supporting dependency seams, not separate user-facing protocols.

### Collection and entity semantics

- The default collection scope is all entities. Explicit excludes always win.
- Entity behavior is selected by explicit per-entity override, then domain/device-class profile, then generic profile.
- Initial profiles are numeric, binary, battery, energy, climate, contact/security, light/media, and generic.
- Numeric profiles collect state changes and five-minute snapshots by default. Snapshot intervals and thresholds can be overridden per entity.
- Snapshots are measurements and never imply a transition.
- State, unknown, and unavailable intervals are measured. Attribute-only changes are not state transitions.
- Safe metadata is stored by default. Additional attributes are per-entity opt-in and allowlisted.
- Profile changes increment `profile_version`. Recompute reads only raw data within the 30-day retention window; older rollups retain the version that produced them.

### Data flow and durability

- The integration generates stable event IDs, normalizes timestamps to UTC, and batches at 30 seconds or 100 events, whichever occurs first.
- The first release deliberately has no durable queue on the Raspberry Pi. An unacknowledged in-memory batch may be lost during sidecar downtime.
- A heartbeat is sent approximately every minute. The sidecar records a data gap when the expected heartbeat interval is exceeded.
- The sidecar validates a complete batch before committing it. Event IDs are unique and deduplicated transactionally.
- The sidecar is the canonical analytics data owner. It does not pull HA REST history or subscribe to HA WebSocket in the first release.
- Raw events and closed intervals are retained for 30 days. Daily rollups, report runs, and LLM attempt metadata are retained for 2 years.
- SQLite uses short transactions, WAL mode, busy timeout, and foreign keys. Only the Go service writes the analytics database.

### Time and report behavior

- UTC is the timestamp storage truth. The report window uses Home Assistant's local timezone.
- One active daily report time is configured, defaulting to 08:00; allowed initial choices are 08:00, 12:00, 18:00, and 22:00.
- A daily report analyzes the previous complete local calendar day. The report stores the actual UTC bounds and timezone, including DST effects.
- Manual reports accept a local ISO date (`YYYY-MM-DD`).
- Report jobs are idempotent by `report_date`; the job stores the configuration/profile snapshot used for the run.
- The report state sequence is requested, collecting, deterministic-ready, AI-pending, AI-running, completed, AI-retry-scheduled, or AI-failed.
- A completed report is not overwritten by ordinary polling or retry. Any future forced recompute must be an explicit operation outside this first release.

### Deterministic analysis and risk

- Deterministic calculations run before Gemini and produce evidence IDs.
- Initial safety coverage includes leak, smoke, gas, door/window, lock/security, abnormal temperature, long unavailable state, and abnormal energy.
- Risk levels are high, medium, and low. High risk can notify immediately with cooldown; medium and low are included in the daily report.
- A 14-day baseline period is required for statistical findings. Fixed safety rules still operate during cold start.
- Coverage, baseline age, data gaps, sample counts, and limitations are part of the report and confidence calculation.
- There is no semantic fixed cap on anomaly or suggestion count. Technical limits for SQLite queries, HTTP bodies, Gemini context, and Telegram messages remain enforced; truncation must be explicit in `data_quality`.

### Gemini integration

- The external service calls Gemini directly with the official `google.golang.org/genai` Go SDK. The dependency is pinned below major version 2 until qualified otherwise.
- The first backend is the Gemini Developer API. Vertex/enterprise authentication is out of scope.
- The model is configured from HA, defaults to Flash, and can be any supported Gemini model including Pro.
- The API key is read only by the sidecar from environment or Docker secret. It never enters HA report attributes, API responses, or logs.
- Gemini receives only a bounded, pseudonymous, redacted aggregate DTO with deterministic evidence and limitations; it does not receive a raw event dump or arbitrary attributes.
- Structured JSON output is validated against the versioned report schema. Invalid JSON, unsupported enums, missing fields, or invalid evidence references fail the attempt.
- Human prose is Traditional Chinese; field names and enums are stable English values. Gemini is advisory and cannot request or execute HA actions.
- Timeout, connection reset, rate limit, and provider 5xx failures use retry times of 15 minutes, 1 hour, 4 hours, and next day. Authentication, invalid request, quota configuration, and schema failures are not blindly retried.
- Each LLM attempt records provider, model, request ID, attempt number, timings, input hash, usage if available, status, and error code.

### REST contract

- Protected endpoints use a shared Bearer token, `X-Request-ID`, and `X-Homekeeper-Contract-Version`.
- The contract includes event ingest, heartbeat ingest, report request, report status, report result, and health/readiness endpoints.
- Event batches have a maximum of 100 records and are all-or-nothing after validation.
- Report requests accept a local `report_date` and return an existing job when that date is already known.
- Error mapping distinguishes invalid input, authentication failure, contract conflict, size limits, backpressure, service errors, and readiness failure.
- The machine-readable OpenAPI and JSON Schema documents are the contract source of truth. New optional fields may be added compatibly; breaking changes require a new major API version.

### Home Assistant lifecycle and HACS

- The integration must remain loaded while the sidecar is temporarily offline and expose a degraded/reconnecting state.
- Options changes rebuild the HTTP client and apply the new sidecar URL, token, timeout, report time, exclusions, and profile settings.
- Unload/reload cancels event listeners, heartbeat/report polling, retry/reconnect tasks, and closes HTTP resources.
- The integration must not store the shared token in sensor attributes or logs. The sidecar must not store a Home Assistant token.
- The monorepo must keep all HACS runtime files inside the one integration directory and keep Go source/build material outside it.
- HACS publishing follow-up includes required issue-tracker metadata, brand/icon assets, documentation for sidecar installation, separate Go build/release checks, and Home Assistant validation.

## Testing Decisions

Tests should verify externally observable behavior at the highest seam possible. They should not assert private helper structure, SQL statement ordering, or the choice of Go package when the REST behavior and domain outcomes are unchanged.

- **REST contract tests:** Send fixture batches, heartbeats, duplicate IDs, invalid payloads, report requests, and polling requests through the sidecar HTTP boundary. Verify status codes, response shapes, idempotency, all-or-nothing validation, authentication, contract-version rejection, and bounded errors.
- **End-to-end report tests:** Use a fake `LLMClient` and a test SQLite database. Ingest a deterministic event fixture, request a date, advance the report job, and verify the complete report, evidence links, profile version, data quality, and Telegram-ready result behavior.
- **HA integration tests:** Use a fake sidecar HTTP server. Verify event batching at the 30-second/100-event boundaries, heartbeat scheduling, timeout behavior, bounded retry, degraded state, report polling, options reload, unload cleanup, redaction, and Telegram message splitting.
- **Domain tests:** Test profile selection precedence, numeric snapshots, state intervals, unknown/unavailable durations, daily local-date boundaries, DST window metadata, 14-day cold start, fixed safety rules, baseline comparison, cooldown, risk levels, and data-gap confidence using pure fixtures.
- **SQLite integration tests:** Verify migrations, unique event IDs, duplicate ingest, transactional batch behavior, report job claiming, expired AI lease recovery, retention pruning, profile-versioned rollups, and indexes needed for report windows.
- **Gemini adapter tests:** Inject fake responses for valid structured JSON, malformed JSON, schema-invalid output, missing evidence, timeout, connection reset, 429, 5xx, authentication error, and quota/configuration error. Verify retry classification and that prompts/logs do not contain redacted secrets.
- **Schema tests:** Validate representative ingest and report fixtures against the JSON Schemas and verify report findings use only known evidence IDs and allowed enum values.
- **Resource tests:** Run a sidecar soak test with the expected low-volume household workload and approximately 1 GB memory limit. Verify bounded HTTP body/query/report sizes and that the HA integration does not perform analytics database writes.
- **Manual live API test:** Keep real Gemini calls opt-in and outside ordinary CI. Pin the SDK version and record the selected model/capability behavior when running the live check.

The current repository has no test suite, HA fixtures, CI, or prior implementation test patterns to preserve. New tests should therefore establish contract and external-behavior coverage before adding broad implementation-specific tests.

## Out of Scope

- Migrating or interpreting existing Home Assistant Store data.
- Writing analytics data to the Raspberry Pi, Home Assistant Recorder private database, or a durable Pi-side queue in the first release.
- Sidecar pull collection through Home Assistant REST, WebSocket subscriptions, Recorder history backfill, or Logbook.
- A second analytics database engine, Prometheus, InfluxDB, TimescaleDB, or a hosted analytics platform.
- Multiple households, multiple HA config entries, multi-tenant authorization, or a public Internet API.
- Multi-provider LLM routing, LiteLLM, Vertex AI/enterprise Gemini authentication, local LLMs, or automatic provider fallback.
- Automatic Home Assistant actions, service calls, automations, emergency response, or executing AI suggestions.
- Backup, restore, disaster recovery, or historical data export as a product requirement.
- A semantic hard cap on anomalies or suggestions; technical limits and explicit truncation remain required.
- A HACS mechanism that installs, starts, supervises, or updates the Go sidecar.
- A real-time dashboard, interactive chat interface, streaming report UI, or mobile application.
- Rewriting Home Assistant Recorder behavior or depending on its private schema.

## Further Notes

- The sidecar being offline is an expected degraded condition, not an HA setup failure. Data gaps are explicit and should lower confidence.
- The simpler alternative of having HA keep the analytics rollup and call a one-shot analysis endpoint was considered. It was not selected because the user explicitly wants analytics storage outside the Raspberry Pi and lower SD-card wear; the sidecar therefore remains the canonical analytics owner.
- The one seam used for product integration is the HA-to-sidecar REST contract. Internal storage and LLM ports are kept deliberately small and are tested with adapters.
- HACS can coexist with Go source in one repository, but the custom integration directory must remain self-contained and the Go sidecar must be independently packaged and released.
- The existing integration's manifest and repository metadata need HACS quality follow-up before publishing the rewrite, including issue-tracker metadata, brand/icon assets, sidecar instructions, and separate Python/Go CI paths.
- The report JSON is complete and available through the API; Home Assistant's sensor and Telegram views may be compact renderings. Any technical truncation must be disclosed in `data_quality`.
- No implementation code is part of this spec publication. Once an agent claims the spec, implementation should proceed in vertical slices through the REST seam: contract/health, ingest/heartbeat, storage/aggregation, report job, Gemini adapter, then HA rendering and packaging.

## Comments

- The user reviewed the architecture and Chinese reading documents and confirmed there were no issues.
- The user confirmed the primary seam: `Home Assistant integration ↔ Go sidecar REST API`.
