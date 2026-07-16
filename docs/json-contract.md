# JSON output contract

This document describes the current-major `ptv --json` contract. JSON is a
command-owned normalized representation, not a raw PTV or GTFS Realtime
response.

## Shared rules

- A successful leaf command writes exactly one JSON document followed by a
  newline to stdout. Progress, geocoding disclosure, degradation warnings, and
  errors use stderr. Interactive authentication prompts also move to stderr
  when `--json` is set.
- A failed command does not manufacture an empty success document. It returns
  non-zero and reports a sanitized error on stderr; signed URLs and credentials
  are never included.
- GTFS source provenance exposes only the URL origin. Userinfo, path, query, and
  fragment are treated as potentially signed/secret request material.
- Presentation text has leading/trailing and repeated whitespace normalized
  while identifiers retain their source value (apart from surrounding
  whitespace).
- Collection fields are emitted as arrays or objects rather than `null` when a
  command owns the collection. Optional scalar/object evidence uses
  `omitempty` or JSON `null` where the established contract requires a nullable
  field.
- PTV route-stop `stop_sequence` is preserved. Zero means the upstream response
  did not establish an order; human tables render it as `-` after all positive
  sequence rows.
- Fields named `*_utc` are RFC 3339 UTC timestamps. Journey, departure, and
  disruption display timestamps use RFC 3339 with the Victorian offset and the
  containing document declares `"time_zone":"Australia/Melbourne"`. Staffing
  hours are upstream clock strings, not instants.
- Additive fields may be introduced in the current major. Removing or changing
  the meaning of an established field requires a next-major change.

## Identifier namespaces

Matching strings do not make identifiers interchangeable.

| Explicit field | Namespace |
| --- | --- |
| `ptv_stop_id`, `ptv_route_id`, `ptv_direction_id` | PTV Timetable API numeric identifiers |
| `ptv_run_ref` | PTV Timetable API run reference |
| `ptv_disruption_id` | PTV Timetable API disruption identifier |
| `gtfs_stop_id`, `gtfs_trip_id`, `gtfs_route_id`, `gtfs_block_id` | static GTFS source identifier, namespaced by the local compiler where applicable |
| `feed_entity_id` | GTFS Realtime `FeedEntity.id` |
| `public_label` | public vehicle label used for user identity and GTFS-R matching |
| `ptv_vehicle_descriptor_id` | public descriptor value exposed by PTV v3 |

Unqualified `stop_id`, `route_id`, `direction_id`, `run_ref`, `trip_id`,
`disruption_id`, and `vehicle_id` fields are retained for current-major
compatibility. Corrected command DTOs also emit the explicit source-qualified
field. In `plan`, the legacy `index` is generation-local and must not be stored
as a durable identifier. `route_gtfs_id` is the value named that way by the PTV
API; it is not proof of a join to a local static generation.

GTFS-R private vehicle IDs are neither matched nor emitted. A PTV `run_ref`, a
static `trip_id`, and a GTFS-R `feed_entity_id` are never joined by string
equality.

## Command documents

| Command | Top-level JSON |
| --- | --- |
| `version` | `version`, `commit`, `date` |
| `auth status` | `configured`, and when configured `source`, `dev_id` |
| `auth check` | `route_types[]`, `status` |
| `auth login/logout` | credential category plus `verified`/`stored` or `removed` |
| `auth opendata status` | `configured`, `has_api_id`, `api_id_transmitted` |
| `auth opendata check` | `ok`, `feed_id`, `entities`, `authentication_header`, `api_id_transmitted` |
| `auth opendata login/logout` | credential category plus result; login also states `api_id_transmitted:false` |
| `search` | `stops[]`, `routes[]`, `outlets[]`, `status` |
| `lines` | `routes[]`, compatibility `route`, `status` |
| `lines show` or `lines <route>` | `route`, `directions[]`, `stops` keyed by direction |
| `stops near` / `stops on` | `stops[]`, `status`; `stops near` adds `attribution` when geocoding contributed |
| `station` | `stop` (nested location, amenities, accessibility, staffing, routes), `disruptions`, `status` |
| `next` and mode-scoped `next` | `departures[]`, expanded `stops`, `routes`, `runs`, `directions`, `disruptions`, `status`, `time_zone` |
| `train/tram/bus/vline <route>` | `route`, `directions[]`, `stops`, `disruptions[]`, `time_zone` |
| `train/tram/bus/vline lines` | `routes[]`, compatibility `route`, `status` |
| `vehicle` | query/match evidence, explicit PTV fields, separate `position` and `gtfs_realtime`, optional next/last stop, `warnings[]` |
| `plan` | `legs[]`, `depart`, `arrive`, `transfers`, optional `disruptions[]`, `time_zone`, `attribution[]`, `warnings[]` |
| `disruptions` | mode-keyed `disruptions`, `status`, `time_zone` |
| `fare` | endpoint-specific `FareEstimateResultStatus` and `FareEstimateResult`; this endpoint does not return the generic PTV `status` object |
| `outlets` | `outlets[]`, `status` |
| `gtfs update` | published generation identity, archive retention, persisted counts, coverage, and provenance |
| `gtfs status` | generation, persisted dataset state/counts, and `freshness`; missing/legacy states return `ingested:false` plus `action` |
| `gtfs check` | source-keyed freshness report; `--force` bypasses success throttling or failure backoff |
| `gtfs realtime` | local `feeds[]` catalog, or fetched `feeds[]` status records with feed timestamp, entity-kind counts, and optional `sample_public_label` / `sample_static_gtfs_trip_id` evidence |

Map keys inherited from a PTV expansion are lookup keys only; consumers should
use the explicit identifier inside each mapped value as identity.

## Enums and warning evidence

- Static-feed freshness is one of `current`, `changed`, `stale`, or `unknown`.
  A failed check or missing comparable validator cannot be `current`.
- GTFS-R observation/feed freshness is `current`, `stale`, or `unknown`.
  `current` requires a present, non-future observation no more than 90 seconds
  old; combined state is conservative across entity and feed evidence.
- `plan` pickup/drop-off policies use GTFS values: `0` regular, `1` forbidden,
  `2` phone agency, and `3` coordinate with driver. Values 2/3 are considered
  only with `--allow-conditional`, and affected legs set `conditional:true`.
- `stay_onboard:true` is emitted only for a resolved GTFS type-4 linked-trip
  continuation. Equal `block_id` values alone never set it; type 5 prohibits it.
- `plan.warnings` records optional disruption/freshness degradation and
  conditional-service notices. Those warnings are also sent to stderr so a
  human notices them without parsing JSON. `vehicle.warnings` explains lookup
  limits and enrichment failures without changing a not-found or stale result
  into an API success claim.

Golden contract tests under `cmd/testdata` cover representative command-owned
documents. Tests also assert that warning output cannot corrupt JSON stdout and
that private GTFS-R vehicle IDs are absent.
