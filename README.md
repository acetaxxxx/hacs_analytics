# Home Daily Report

Home Daily Report is a Home Assistant custom integration that watches selected
entities, builds compact daily rollups, and can pass those rollups to Home
Assistant's `ai_task.generate_data` action for a daily AI summary.

The integration does not need a Home Assistant long-lived token because it runs
inside Home Assistant. It also does not store or call any AI provider credentials
itself. Configure your preferred AI Task entity in Home Assistant, then let this
integration send structured rollup data to `ai_task`.

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

- Included devices. If none are selected, all matching domains are tracked.
- Included entities, for helpers such as `input_boolean.guest_mode` or `input_select.home_mode`.
- Included domains, selected from a multi-select dropdown.
- Entity exclude globs, for example `sensor.time*,sensor.date*`.
- Daily report time, selected with a time picker.
- Rollup retention days.
- Whether to call `ai_task.generate_data`.
- Notification service, for example `persistent_notification.create` or `notify.notify`.

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

This is an MVP. It intentionally favors cheap event-based daily rollups over
scanning the full Home Assistant recorder database. Long-term recorder
statistics can be added later for more accurate time-weighted averages.
