# Version entity profiles and recompute the retained raw window

Status: accepted

Entity profile changes take effect for newly received data and trigger a recomputation for the raw observations still retained by the analytics store. Rollups older than the raw retention window remain associated with their previous profile version. This preserves the current 30-day raw retention and 2-year rollup retention without promising impossible reconstruction of deleted observations.

## Consequences

Rollups and reports must retain the profile version used to produce them. A profile change is observable in report history, and recomputation is bounded to the retained raw window rather than becoming an unbounded background migration.
