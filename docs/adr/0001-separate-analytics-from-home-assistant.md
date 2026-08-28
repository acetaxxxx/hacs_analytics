# Separate analytics from Home Assistant

Status: accepted

The analytics system will run as a Go service outside the Home Assistant host, while a thin Python custom integration remains at the Home Assistant edge. This keeps high-volume analytics storage, aggregation, and Gemini calls away from the Raspberry Pi's SD card and Home Assistant lifecycle. Home Assistant remains responsible for observing the home and delivering notifications through its configured Telegram integration.

## Considered Options

- Run collection, analytics, and storage entirely inside Home Assistant.
- Run a separate analytics service that receives household observations from Home Assistant.

## Consequences

The two runtimes must share a versioned contract. If the external service is unavailable, observations may be lost by design; the service must expose the resulting data gap rather than pretending that the report is complete.
