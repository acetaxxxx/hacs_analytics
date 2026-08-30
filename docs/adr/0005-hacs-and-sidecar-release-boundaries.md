# Keep HACS packaging and sidecar release boundaries separate

Status: accepted

The repository may contain both the Home Assistant custom integration and the Go analytics service, but they are two independently deployed runtimes. HACS packages and updates only the single integration under `custom_components/home_daily_report/`. The Go sidecar has its own `go.mod`, build artifacts, Windows/Docker installation path, health checks, and release versioning.

## Considered Options

- Put the Go service in the HACS integration directory and let HACS install it.
- Keep both projects in one monorepo while releasing and deploying them independently.
- Split the projects into separate repositories immediately.

## Consequences

The monorepo keeps shared contracts and documentation local while preserving clear ownership. HACS cannot compile, start, stop, monitor, or update the sidecar, so deployment documentation must describe both flows. The integration must treat sidecar absence as a degraded/reconnecting state rather than making Home Assistant setup fail.

The integration owns its HTTP client and any optional WebSocket connection. Config-entry reload/unload must cancel reconnect tasks, flush or discard the bounded in-memory batch according to policy, and close the client/session. If a future sidecar pull mode uses the Home Assistant REST API, it must use a Home Assistant token only at runtime and never persist that token in the sidecar.

The current repository also needs HACS release-quality follow-up: its manifest should include the required issue tracker metadata and the project should provide the expected brand/icon assets before publishing the rewritten integration.
