# GTFS static / GTFS-R Phase 0

Validation command: `ptv gtfs validate-realtime`

The command is implemented and uses the metro trip-updates catalog feed. It
requires the documented Open Data `KeyID` credential, reads the local immutable
generation, and reports joinable updates, matched updates, match rate, sample
IDs, and the selected strategy as JSON or human output.

The fresh local build was run against the live metro trip-updates feed on
2026-07-24 using the configured Open Data `KeyID` through the explicit
credential path. It found 185 updates, all 185 joinable, and matched all 185
(`match_rate: 1.0`) with no warnings or unmatched IDs. The separately reported
legacy Open Data token is retained for compatibility but is not transmitted.
Realtime readiness therefore passes the 95% gate for this feed and strategy.
The production join strategy is intentionally fixed to:

```
feed-local trip_id + start_date
  -> source_trip_id in the selected feed mode
  -> namespaced static ID {feedMode}:{source_trip_id}
```

There is no silent namespace fallback. Missing trip identity or start date is
unknown/unjoinable. If a future live run is below the 95% gate, the command
reports the warning and an alternative strategy must be selected and fixture
tested before changing realtime joins.

Fixture evidence: `internal/gtfsrt/realtime_normalization_test.go` verifies
feed-local trip/route/stop identity, explicit schedule relationships, selected
English alert translations, and freshness; `internal/gtfs/queries_test.go`
verifies namespaced static resolution and generation-local key joins.
