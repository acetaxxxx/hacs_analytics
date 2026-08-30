# Entity profiles

Profiles describe what data is useful and what rules are safe for an entity. They are not HA domains and should not assume every entity exposes the same attributes. Automatic selection is followed by an optional per-entity override.

## Selection

```text
excluded entity                              -> no collection
entity override                              -> override profile
domain + device_class match                  -> automatic profile
domain match                                 -> automatic profile
otherwise                                    -> generic profile
```

The UI defaults to selecting all entities. Users can exclude complete devices,
exact entities, or entity glob patterns, and can opt an entity into safe
attributes. An excluded entity always wins over an otherwise matching profile.

## Initial profiles

| Profile | Inputs | Default behavior |
| --- | --- | --- |
| `numeric` | parseable numeric state, unit, device class | state changes plus five-minute snapshots; min/max/average/delta and duration |
| `binary` | on/off and equivalent states | transitions, on/off duration, rapid cycling |
| `battery` | percentage-like numeric state | low-battery thresholds, trend, duration below threshold |
| `energy` | energy/power numeric state and unit | consumption/demand rollup, gaps, unusual change |
| `climate` | temperature/humidity and safe mode state | comfort range, duration outside range, trend |
| `contact_security` | open/closed/locked and safe security state | open duration, overnight change, availability |
| `light_media` | on/off and selected mode/volume metadata | active duration, unusual overnight activity |
| `generic` | state string and timestamps | transitions, state duration, availability; no invented numeric meaning |

Each entity may override snapshot interval, thresholds, allowed state set, timezone interpretation, and safe metadata fields. A profile must declare units before numeric comparisons; values with incompatible units are retained as observations but excluded from that rule.

## Attributes and privacy

The default profile stores only stable metadata needed to interpret a value: domain, device class, unit, and a safe display label. Attribute changes are not treated as state transitions. An entity-level opt-in may select additional allowlisted attributes. Sensitive attributes, names, addresses, tokens, free-form text, and camera/media content are redacted before storage and before Gemini input.

## Versioning

Every collected event and rollup stores `profile_version`. Changing an entity rule increments its version. Recompute processes only raw events in the retained 30-day window and writes a new versioned rollup; 2-year rollups older than the raw window remain associated with their historical version.

## Snapshot semantics

Snapshots are measurements, not transitions. They improve numeric time coverage but cannot prove what happened between two snapshots. A report carries sample count and coverage so Gemini can distinguish evidence from inference.
