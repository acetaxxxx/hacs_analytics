# Security and privacy

## Trust boundary

The external service is reachable only from the trusted home LAN. It is not exposed to the public Internet. Every API request uses a shared Bearer token and a contract-version header. The token is configured on both sides, stored outside source control, and compared using a constant-time operation.

This token authenticates the integration to the sidecar; it is not a Home Assistant access token and is never sent to Gemini. The sidecar does not store a Home Assistant token and does not call HA APIs in the first release.

## Secret handling

- Gemini API key: external Go container environment/Docker secret only.
- Shared REST token: Home Assistant secret configuration and external service environment/secret.
- Logs: never include Authorization headers, API keys, full prompts, full reports, or arbitrary attributes.
- Errors returned to HA use stable public error codes; provider/SQLite internals stay in local logs.

## Collection minimization

The default scope selects all entities but applies an explicit exclusion list. Generic entities contribute state and timing only. Attribute collection is opt-in per entity and restricted to an allowlist. Before persistence and again before Gemini input, the service removes tokens, credentials, free-form text, addresses, names that are not safe labels, camera/media payloads, and other sensitive values.

Gemini receives pseudonymous stable entity IDs and safe labels, bounded aggregates, intervals, and deterministic evidence. It does not receive the HA database, raw event dumps, or a credential. The report retains evidence IDs rather than copying unbounded source data.

## Operational limits

The API enforces request body, batch-count, string-length, and report-size limits. A large report is split by the HA Telegram renderer rather than causing an unbounded sidecar response. SQLite retention is enforced by the external service. There is no backup requirement in the first release; users should understand that deleting the external SQLite volume deletes analytics history.

## Safety semantics

Homekeeper is advisory. A high-risk item is a prompt for the user to verify the underlying entity, not an automatic emergency response. Deterministic evidence, confidence, data gaps, baseline age, and last-seen time are visible in the report so a missing sensor is not mistaken for a safe state.
