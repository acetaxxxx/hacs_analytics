# Home Daily Report

Homekeeper is a read-only Home Assistant housekeeper. A thin Home Assistant
integration sends selected state observations to an independently deployed Go
sidecar. The sidecar owns SQLite aggregation, deterministic anomaly/risk
checks, and optional structured Gemini reports. It never executes a Home
Assistant action.

Home Assistant runs on the Raspberry Pi and does not write the analytics
database to its SD card. Run `services/analyticsd` in Docker on the external
Windows computer (or another always-on machine). Telegram remains configured in
Home Assistant; the sidecar does not need Telegram credentials.

## What It Tracks

- State transition counts, such as `off => on` or `closed => open`.
- Total state changes per entity and per day.
- `unknown` and `unavailable` occurrences.
- Numeric samples for sensors, including min, max, average, and large jumps.
- 7, 14, and 28 day trend windows based on stored daily rollups.

## Installation

### HACS Custom Repository

1. Add this repository to HACS as an integration custom repository.
2. Install **Home Daily Report**.
3. Restart Home Assistant.
4. Go to **Settings > Devices & services > Add integration**.
5. Search for **Home Daily Report**.

### Manual Install

Copy `custom_components/home_daily_report` into your Home Assistant
`custom_components` directory, then restart Home Assistant.

## Configuration

The config flow asks for:

- Included devices. If none are selected, all entities in the selected domains are tracked.
- Included entities, for helpers such as `input_boolean.guest_mode` or `input_select.home_mode`.
- Included domains, selected from a multi-select dropdown and all selected by default.
- Entity exclude globs, for example `sensor.time*,sensor.date*`.
- Daily report time: 08:00, 12:00, 18:00, or 22:00.
- Rollup retention days.
- Existing Home Assistant notification service, for example `notify.telegram`.
- Sidecar URL, shared token, timeout, and Gemini model (Flash is the default).

### External sidecar

Run the sidecar separately; HACS does not install or supervise it. For a
published release, pull the image produced from the Git tag:

```sh
docker pull ghcr.io/acetaxxxx/homekeeper-analyticsd:<git-tag>
```

To build manually instead:

```sh
cd services/analyticsd
docker build -t homekeeper-analyticsd .
docker run -d --name homekeeper-analyticsd -p 8080:8080 \
  -e HOMEKEEPER_SHARED_TOKEN='use-a-long-random-token' \
  -e GEMINI_API_KEY='your-gemini-api-key' \
  -e HOMEKEEPER_TIMEZONE='Asia/Taipei' \
  -v homekeeper-data:/data homekeeper-analyticsd
```

Enter `http://<computer-ip>:8080`, the same shared token, and the selected
Gemini model in the integration options. Keep the port on the trusted LAN and
do not commit either secret.

## Services

### `home_daily_report.generate_report`

Generate a report manually.

```yaml
action: home_daily_report.generate_report
data:
  date: "2026-06-18"
  use_ai: true
  notify: true
```

If `date` is omitted, the integration reports yesterday.

## Events

Every report fires `home_daily_report_report_ready`.

```yaml
trigger:
  - platform: event
    event_type: home_daily_report_report_ready
```

The event payload includes the rollup report and, when available, AI output.

## Notes

The sidecar retains raw observations for 30 days and derived rollups/reports for
2 years. The first release intentionally does not migrate the old Home
Assistant Store data, read the private Recorder database, or maintain a durable
queue on the Raspberry Pi. An unavailable sidecar is shown as degraded and a
missing heartbeat becomes an explicit `data_gap`.
