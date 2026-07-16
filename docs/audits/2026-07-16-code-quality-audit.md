# Code quality audit — 16 July 2026

**Status:** pre-remediation baseline. The finding register records the code as
first audited; implementation and final evidence are tracked in the
[remediation project log](../../specs/audit-remediation/PROJECT_LOG.md).

## Outcome

The CLI has a sound basic shape: package boundaries are clear, the HMAC signer
matches PTV's contract, network downloads are streamed and capped, and the
existing build, vet, and unit-test gates pass with a coherent Go toolchain.
The audit nevertheless found correctness defects in the journey graph, material
contract drift in PTV/GTFS Realtime models, and a measured planner startup cost
that is not viable for an interactive CLI.

No production logic had been changed when the findings below were recorded.
Remediation was then implemented against the adjacent numbered specs; this
document intentionally preserves the before-state instead of rewriting the
finding evidence as though the defects never existed.

Priority means:

- **P1:** can produce a wrong journey, misleading live state, invalid API
  behavior, data loss, or an unusable primary workflow; remediate before adding
  planner features.
- **P2:** important reliability, portability, policy, or maintainability work;
  schedule after the P1 dependency it names.

## Evidence collected

- Traced every command family through configuration, PTV v3, GTFS Realtime,
  static GTFS ingest, timetable construction, CSA, geocoding, and rendering.
- Compared implementation with the current authoritative documents listed
  below before evaluating behavior.
- Ran `go build ./...`, `go vet ./...`, and `go test ./...`; all passed after
  correcting a machine-level mixed Go binary/GOROOT condition.
- Used the owner-authorized `.env` only through `ptv --env-file .env`; its
  contents and signed URLs were never read or printed.
- Live PTV `station` and Open Data bus vehicle-position requests succeeded. The
  station response confirmed that requested facilities are discarded by the
  local DTO. The GTFS-R response contained 1,621 entities and a current header
  timestamp.
- Benchmarked a local production-shaped dataset: 32,097 stops, 401,905 trips,
  and 15,297,769 stop times.

| Local plan case | Result | Wall time | Peak memory |
| --- | --- | ---: | ---: |
| Earliest arrival, one train leg | Correct route | 68.57 s | not captured |
| Arrive by, one train leg | Correct route | 48.87 s | 575 MB |

Both measurements disabled geocoding, disruptions, and update checks. They
therefore isolate local timetable construction and routing rather than network
latency.

### Post-remediation checkpoint

On the next comparable production generation (14,613,523 stop times), the same
host and command shape produced five-run medians of 0.97 s forward and 1.08 s
arrive-by, with maximum peak RSS of 154.75 MiB and 162.89 MiB respectively.
This is 70.7x and 45.3x faster than the baselines above and meets the specified
three-second/256 MiB budget. The original byte-identical archive was not
retained; counts and all raw samples are disclosed in the project log.

The final reviewed binary also passed owner-authorized live compatibility
probes on 16 July: PTV Stop Details returned a healthy normalized Flinders
Street response, and the documented GTFS-Realtime `KeyID` request returned a
current full bus vehicle-position dataset with 1,595 entities. Credential
values, signed URLs, and private vehicle identifiers were not exposed. Full
gate and live evidence is recorded in the project log.

## Finding register

| ID | Priority | Finding | Evidence | Remediation |
| --- | --- | --- | --- | --- |
| F01 | P1 | `transfers.txt` is reduced to generic walks. Forbidden type 3 transfers become allowed; route/trip precedence, same-stop change time, and linked trips are lost. | `internal/gtfs/{schema.go,ingest.go,timetable.go}` | [Spec 001](../specs/001-gtfs-routing-correctness.md) |
| F02 | P1 | Station hierarchy and directed `pathways.txt` are ignored and replaced with symmetric straight-line shortcuts, including across physical barriers. | `internal/gtfs/ingest.go` | [Spec 001](../specs/001-gtfs-routing-correctness.md) |
| F03 | P1 | CSA relaxes only one footpath hop although its graph is not transitively closed; same-stop minimum transfers are bypassed. | `internal/router/csa.go` | [Spec 001](../specs/001-gtfs-routing-correctness.md) |
| F04 | P1 | `pickup_type` and `drop_off_type` are discarded, allowing boarding and alighting where GTFS prohibits it. | `internal/gtfs/{schema.go,ingest.go,timetable.go}` | [Spec 001](../specs/001-gtfs-routing-correctness.md) |
| F05 | P1 | Service times are anchored at local midnight rather than GTFS's local-noon-minus-12-hours rule, producing DST errors. The horizon omits next-day services, while raw trip IDs can merge the current and previous service-day instances. | `internal/gtfs/timetable.go`, `internal/router/csa.go` | [Spec 001](../specs/001-gtfs-routing-correctness.md) |
| F06 | P1 | Timetable construction loads and transforms most of a 15-million-row dataset on every command; arrive-by re-sorts a reversed copy. | measured; `internal/gtfs/timetable.go`, `internal/router/csa.go` | [Spec 002](../specs/002-planner-performance-and-store-lifecycle.md) |
| F07 | P1 | GTFS ingest can commit an empty/incomplete dataset, retains indexes during bulk reloads, and commits data before index creation. Migration errors are ignored. | `internal/gtfs/{ingest.go,store.go,schema.go}` | [Spec 002](../specs/002-planner-performance-and-store-lifecycle.md) |
| F08 | P1 | PTV Stop Details is modeled with nonexistent top-level coordinates and omits official location, amenities, accessibility, staffing, and disruption fields. | `internal/ptvapi/models.go`, `cmd/station.go` | [Spec 003](../specs/003-external-api-contracts.md) |
| F09 | P1 | PTV `run_ref`, static-GTFS `trip_id`, and GTFS-R `FeedEntity.id` are treated as interchangeable; a static trip ID can be emitted as a PTV run reference. | `internal/gtfsrt/client.go`, `cmd/vehicle.go` | [Spec 003](../specs/003-external-api-contracts.md) |
| F10 | P1 | GTFS-R matches are labeled `current` without validating feed/entity timestamps, and an upstream-declared internal vehicle ID is shown to users. | `internal/gtfsrt/client.go`, `cmd/vehicle.go` | [Spec 003](../specs/003-external-api-contracts.md) |
| F11 | P1 | Public/static commands and local planning are coupled to PTV Timetable credentials even when live overlays are optional. | `internal/config/config.go`, `cmd/{gtfs.go,plan.go}` | [Spec 004](../specs/004-runtime-geocoder-and-freshness-contracts.md) |
| F12 | P2 | GTFS-R auth tries three header spellings on every failure, amplifying valid and invalid requests; broad official PTV endpoints are replaced by serial per-mode calls. | `internal/gtfsrt/client.go`, `cmd/vehicle.go` | [Spec 003](../specs/003-external-api-contracts.md) |
| F13 | P2 | Nominatim rate limiting is per process rather than aggregate, its endpoint is not runtime-switchable, and attribution/privacy disclosure is absent. | `internal/geocode/geocode.go`, plan/stops output | [Spec 004](../specs/004-runtime-geocoder-and-freshness-contracts.md) |
| F14 | P2 | Freshness uses ingest time rather than publication/coverage, is not keyed by source URL, collapses unknown into current, and retries failed HEAD requests on every invocation. | `internal/gtfs/freshness.go` | [Spec 004](../specs/004-runtime-geocoder-and-freshness-contracts.md) |
| F15 | P2 | A fixed one-cell spatial search can miss valid 250 m transfer pairs at Victorian longitudes. Coordinate origins/targets also omit access/egress walking cost. | `internal/gtfs/ingest.go`, `cmd/plan.go` | [Spec 001](../specs/001-gtfs-routing-correctness.md) |
| F16 | P2 | User-facing times use `time.Local` in several commands; process cancellation is not propagated; JSON output is an undocumented mix of upstream and ad hoc shapes. | `cmd/{next.go,vehicle.go,root.go,helpers.go}` | [Specs 003](../specs/003-external-api-contracts.md) and [004](../specs/004-runtime-geocoder-and-freshness-contracts.md) |
| F17 | P2 | HTTP response reads, inner ZIP materialization, cache replacement, data-path canonicalization, and SQLite contention lack explicit resource/lifecycle contracts. | `internal/{ptvapi,gtfsrt,gtfs,geocode}` | [Specs 002](../specs/002-planner-performance-and-store-lifecycle.md) and [004](../specs/004-runtime-geocoder-and-freshness-contracts.md) |
| F18 | P1 | Final production-feed validation found that Transport Victoria serializes optional `pathways.traversal_time` (and potentially `stair_count`) unknowns as `0`, although the GTFS reference requires positive/non-zero values. Strict rejection made the official feed unusable. | owner-authorized 16 July clean `gtfs update`, Regional Train row 462 | [Spec 001](../specs/001-gtfs-routing-correctness.md) producer adapter: normalize only zero sentinels to absent, then use conservative pathway fallback |
| F19 | P1 | Release-candidate dogfood found that fare JSON fabricated a generic PTV `status` object even though the Fare Estimate schema exposes only `FareEstimateResultStatus`; the resulting empty version/health `0` was false contract evidence. | owner-authorized 16 July live fare probe; current v3 Swagger | [Spec 003](../specs/003-external-api-contracts.md): remove the nonexistent field and validate the endpoint-specific result status |
| F20 | P1 | The release-validation matrix paired tram route 109 with Melbourne University, which it does not serve; route-filtered `next` returned an unexplained empty success instead of distinguishing a mismatch from no current departures. | owner-authorized 16 July candidate dogfood | [Spec 003](../specs/003-external-api-contracts.md): validate membership through cross-mode `StopsForRoute` and correct the skill example |
| F21 | P2 | PTV route-stop responses can mix valid positive sequences with upstream zero values. Sorting zeros last is safe, but rendering `0` as physical order and describing every row as ordered is misleading. | live tram 109/V/Line 1745 candidate dogfood | [Spec 003](../specs/003-external-api-contracts.md): preserve JSON compatibility, render zero as unsequenced, and qualify documentation |

## Confirmed sound behavior

- All 17 implemented PTV paths exist in the current Swagger document. Signing,
  query-array encoding, expansion values, and fare timestamp formatting match.
- The ten hard-coded GTFS-R feed URLs match the official current catalog.
- Weekly calendar activation and `calendar_dates` additions/removals are sound
  for service days that are actually loaded.
- A parse failure rolls back the current GTFS transaction, and the outer GTFS
  download is streamed to a capped temporary file before replacement.
- Nominatim requests use a custom User-Agent, country/viewbox bounds, a result
  limit, and successful-result caching.

## Recommended order

1. Land Spec 001 correctness fixtures and data-model changes.
2. Land Spec 002 staging/migrations, preprocessing, and performance budgets.
3. Land Spec 003 API DTO/identity/freshness work; some pieces can run in parallel
   with Spec 002 after shared model boundaries are agreed.
4. Land Spec 004 configuration split first, then freshness/geocoder/runtime
   hardening.

## Authoritative sources

- [PTV Timetable API v3 Swagger](https://timetableapi.ptv.vic.gov.au/swagger/docs/v3)
  and [signature guide](https://www.vic.gov.au/sites/default/files/2025-06/PTV-Timetable-API-key-and-signature-document.rtf)
- [Transport Victoria GTFS schedule catalog](https://opendata.transport.vic.gov.au/dataset/gtfs-schedule)
  and [GTFS schedule reference](https://gtfs.org/documentation/schedule/reference/)
- [Connection Scan Algorithm paper](https://arxiv.org/abs/1703.05997)
- [Transport Victoria GTFS Realtime catalog](https://opendata.transport.vic.gov.au/dataset/gtfs-realtime),
  [GTFS-R reference](https://gtfs.org/documentation/realtime/reference/), and
  [best practices](https://gtfs.org/documentation/realtime/realtime-best-practices/)
- [Nominatim usage policy](https://operations.osmfoundation.org/policies/nominatim/)
  and [Search API](https://nominatim.org/release-docs/latest/api/Search/)
- [SQLite query planner](https://www.sqlite.org/queryplanner.html),
  [WAL](https://www.sqlite.org/wal.html), and
  [`PRAGMA optimize`](https://sqlite.org/lang_analyze.html)
- [Go release history](https://go.dev/doc/devel/release) and
  [mise project configuration/tasks](https://mise.jdx.dev/configuration.html)
