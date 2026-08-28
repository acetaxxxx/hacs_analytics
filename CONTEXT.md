# Home Assistant Homekeeper

This context describes a household analytics system that observes a Home Assistant environment, identifies unusual or risky conditions, and gives the household a useful summary and suggestions. It distinguishes facts observed in the home from interpretations produced by analysis.

## Observed data

**Entity**:
An addressable Home Assistant state source, such as a temperature sensor, light, door contact, lock, or energy meter.
_Avoid_: Device (a device may expose multiple entities), datapoint

**Observation**:
A timestamped fact about an entity's state or measured value.
_Avoid_: Sample (use only when discussing a numeric measurement), event (an event describes a change)

**Snapshot**:
The current known state of an entity captured at a point in time without implying that the state changed at that moment.
_Avoid_: Transition, initial event

**Transition**:
A change from one entity state to another, including the time at which the change was observed.
_Avoid_: Observation (a transition contains observations but has change semantics)

**Data gap**:
A period during which the analytics system cannot establish that expected Home Assistant data was received.
_Avoid_: Missing event (the system cannot always prove which individual event was missed)

## Interpretation

**Entity profile**:
The interpretation rules for an entity: which observations matter, how they are aggregated, and which conditions are relevant to that entity's type.
_Avoid_: One-size-fits-all rule, entity configuration (configuration may include more than interpretation)

**Rollup**:
A bounded summary of observations and transitions for a defined time window, retaining the measurements needed for later analysis without retaining every original detail.
_Avoid_: Report, raw data

**Baseline**:
The established normal range or behavior for an entity or group of entities, based on enough prior observations to make comparison meaningful.
_Avoid_: Threshold (a threshold is one rule; a baseline is learned or calculated context)

**Anomaly**:
A measurable deviation from an entity's baseline or an explicitly defined safety/operational rule.
_Avoid_: Bug, incident

**Risk**:
A condition that may indicate harm, loss, unsafe operation, security exposure, or a meaningful degradation of the household environment.
_Avoid_: Anomaly (not every anomaly creates a risk), Alert (an alert is a notification)

**Pattern**:
A repeated or correlated behavior found across observations, transitions, or rollups.
_Avoid_: Anomaly (a pattern can be normal), Trend (a trend is one kind of pattern)

**Suggestion**:
A proposed improvement derived from observed behavior, anomalies, risks, or patterns. A suggestion is advisory and does not authorize an action in Home Assistant.
_Avoid_: Command, automation, remediation

## Reporting

**Report window**:
The local-time interval whose observations and rollups are analyzed for one report.
_Avoid_: Query range, calendar day (a report window may later be configurable)

**Daily report**:
The complete report for the previous local calendar day, generated at the one selected household report time.
_Avoid_: Every-run report, rolling report

**Housekeeper**:
The read-only analytical role that summarizes the household, explains anomalies and risks, finds patterns, and offers suggestions.
_Avoid_: Agent (the housekeeper does not autonomously operate Home Assistant), Controller

**Alert**:
A notification about a risk or condition that should be surfaced outside the next daily report.
_Avoid_: Anomaly (an anomaly may remain report-only), Action
