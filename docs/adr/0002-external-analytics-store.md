# Use an external analytics store

Status: accepted

Analytics raw observations and rollups will be stored in a separate SQLite database owned by the external analytics service, not in Home Assistant's Recorder database and not in the integration's local Store. SQLite is suitable for this household's volume; the external location is chosen to isolate high-volume writes from the Raspberry Pi's SD card and Home Assistant's own history database.

## Considered Options

- Store analytics data in Home Assistant's Store.
- Write into Home Assistant's Recorder database.
- Keep a separate analytics SQLite database on the external computer.

## Consequences

The external service owns the analytics data lifecycle, including retention, rollups, and report history. The integration must not depend on Home Assistant's private Recorder schema. No backup is required for the first version.
