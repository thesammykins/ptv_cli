# Audit remediation — project log

This is an implementation aid. [PRODUCT.md](PRODUCT.md) and [TECH.md](TECH.md)
remain the source of truth.

## Baseline

- Audit date: 16 July 2026
- Dataset: 32,097 stops, 401,905 trips, 15,297,769 stop times
- Earliest-arrival audited case: 68.57 s wall time
- Arrive-by audited case: 48.87 s wall time, 575 MB peak RSS
- Initial coherent-toolchain gates: build, vet, test, and race passed
- Toolchain pinned by mise: Go 1.25.12, `GOTOOLCHAIN=local`

The byte-identical June source archive was not retained. Final measurements use
the next owner-authorized production feed on the same host and disclose its
counts below. It is 4.5% smaller by stop-time count; the measured speedups are
large enough that even linear size normalization does not affect the 10x
conclusion.

## Locked decisions

- Feature directory: `specs/audit-remediation` (no issue tracker is in use).
- GTFS v2 databases are immutable generation files selected through an atomic
  current-generation manifest; legacy databases require a one-time re-ingest.
- Router state includes service-instance and transfer context; one label per
  stop is insufficient for route/trip-specific transfer prohibitions.
- Pickup/drop-off policy values 2 and 3 are excluded by default and require an
  explicit conditional-service opt-in.
- Type-4 continuation renders explicit stay-onboard behavior with no transfer
  increment; type 5 prohibits that continuation.
- GTFS-R sends one documented `KeyID` header and no platform bearer token.
- PTV run references, static trip IDs, GTFS-R entity IDs, public labels, and
  internal vehicle IDs remain distinct types.

## Current lanes

| Lane | State | Result |
| --- | --- | --- |
| A0 store foundation | validated | v2 immutable generations, atomic manifest, checked schema, safe staging/download/publication |
| A1 compiler/query | validated | validated GTFS compiler, materialized connections, bounded direction-specific windows |
| A2 router/model | validated | multi-label forward/reverse CSA, walking closure, transfer precedence, explicit continuations |
| B external clients | validated | typed and bounded PTV/GTFS-R clients, exact auth/identity/freshness contracts |
| C runtime services | validated | capability config, policy-safe geocoder, embedded local time, separate freshness state |
| Integration | validated | normalized commands/JSON, staged planner horizons, docs, skills, mise, live/performance probes |

## Completed checkpoints

- `PRODUCT.md` and `TECH.md` created and reviewed.
- Runtime configuration loads paths/endpoints without consulting irrelevant
  credentials; local GTFS and core planning work with no Timetable credential.
- Geocoder foundation supports provider validation/attribution, bounded bodies,
  expiring provider-scoped cache, atomic writes, privacy callback, and
  context-aware local-process coordination. Focused tests pass.
- Shared `internal/localtime` embeds and explicitly uses Australia/Melbourne;
  DST and >24-hour service-anchor tests pass.
- Local GTFS and plan command setup now resolves runtime configuration without
  consulting Timetable or Open Data credentials.
- Plan command integration uses weighted endpoint connectors, conditional
  service opt-in, command context, geocoder disclosure/attribution, normalized
  additive JSON evidence, and explicit zero-leg/stay-onboard output.
- Timetable loading now starts with a direction-specific two-hour window and
  expands to six then 36 hours only after a genuine no-journey result. Exact
  parent-station names resolve to their routable platform descendants.
- The current GTFS producer's non-conforming optional pathway zero sentinels
  are normalized to absent values; negative traversal times remain invalid and
  zero is never interpreted as instantaneous movement.
- Mixed `calendar.txt` plus add-only `calendar_dates.txt` services are accepted,
  while an unknown removal-only service remains invalid.
- PTV/GTFS-R response bodies and errors are bounded and typed, mid-body
  cancellation is preserved, signed URLs are redacted, and operational vehicle
  lookup failures are never reported as vehicle absence.
- Release-candidate dogfood removed a fabricated generic `status` object from
  fare JSON; the official fare endpoint exposes `FareEstimateResultStatus`
  instead. Route-filtered departures now validate that the route serves the
  resolved stop, and upstream zero stop sequences render as unsequenced.
- Repository skills were migrated to `.agents/skills`, corrected against the
  current CLI contracts, and rerun through Plugin Eval at 100/100 with no
  warnings for either skill.
- `mise.toml` exposes portable merge gates plus an extended race, host-timezone,
  CGO-disabled, and Windows cross-build `audit-check`; scoped-trust task
  discovery and `format-check` succeed.

## Production generation validation

- Generation: `g-20260716t032033000000000-24debb4f95367296`, schema v2
- Coverage: 9 July–11 October 2026
- Source archive: 279,898,310 bytes; publication 9 July 2026 01:43:21 UTC
- Counts: 8 feeds, 32,001 stops, 1,073 routes, 6,782 services, 384,722 trips,
  14,613,523 stop times, 98,636 transfers, 5,161 pathways, 14,228,801
  connections, and 12,402 linked-trip rules
- Publication completed only after validation/index/integrity checks. Public
  status output exposes only the source origin, never its path/query/userinfo.

## Production performance

Host: Apple M2 Pro Mac mini (10 cores, 16 GB), macOS/Darwin arm64; Go 1.25.12.
Each sample is a fresh CLI process with a warm filesystem cache and has
geocoding, disruptions, and freshness networking disabled. RSS is the macOS
`/usr/bin/time -l` maximum resident set size.

Harness (repeat each command five times; replace paths for another checkout):

```sh
/usr/bin/time -l env PTV_DATA_DIR=/tmp/ptv-audit-20260716 /tmp/ptv-audit \
  --json plan 'Flinders Street Railway Station' 'Box Hill Railway Station' \
  --depart 09:00 --date 2026-07-16 \
  --no-geocode --no-disruptions --no-update-check >/dev/null
/usr/bin/time -l env PTV_DATA_DIR=/tmp/ptv-audit-20260716 /tmp/ptv-audit \
  --json plan 'Flinders Street Railway Station' 'Box Hill Railway Station' \
  --arrive-by 09:30 --date 2026-07-16 \
  --no-geocode --no-disruptions --no-update-check >/dev/null
```

| Case | Five raw wall/RSS samples | Wall median | Peak RSS maximum | Baseline | Speedup |
| --- | --- | ---: | ---: | ---: | ---: |
| Depart 09:00, Flinders Street–Box Hill | 0.93 s/150.27 MiB; 2.11/152.14; 0.97/154.75; 0.95/151.77; 1.00/152.48 | 0.97 s | 154.75 MiB | 68.57 s | 70.7x |
| Arrive by 09:30, same pair | 2.02 s/142.83 MiB; 1.08/154.33; 1.07/142.25; 1.02/159.84; 1.08/162.89 | 1.08 s | 162.89 MiB | 48.87 s | 45.3x |

All ten commands exited successfully. Both audited cases are below the
three-second median and 256 MiB budgets. A separate Southern Cross–Albury probe
proved the six-hour fallback with a 07:07–10:43 journey; that intentionally
rarer, larger window took 4.86 s and 425.59 MiB and is not represented as common
interactive-path performance.

Focused five-count Go benchmarks on the same toolchain/host:

| Benchmark | Median | Allocation evidence |
| --- | ---: | ---: |
| Representative connection-window load | 35.46 ms/op | 7.77 MB, 91,121 allocs/op |
| Earliest arrival, no contextual rule | 0.899 ms/op | 726 KB, 4,191 allocs/op |
| Earliest arrival, contextual rule | 0.525 ms/op | 726 KB, 4,192 allocs/op |
| Latest departure, no contextual rule | 0.426 ms/op | 726 KB, 4,192 allocs/op |
| Latest departure, contextual rule | 0.443 ms/op | 726 KB, 4,194 allocs/op |

This run establishes the checked-in representative fixture's initial accepted
baseline; future comparisons should use the same Go version and report drift
rather than treating cross-host timing as a deterministic unit-test assertion.

## Live compatibility validation

The final reviewed binary was rebuilt with pinned Go 1.25.12 and exercised on
16 July 2026 through the CLI's explicit `--env-file .env` path. The file and
its values were not inspected, and no credential value was printed.

- PTV Timetable v3 `station 1071 --mode train` succeeded with API health `1`,
  nested Flinders Street coordinates/facilities, normalized disruption output,
  and `Australia/Melbourne` time-zone evidence.
- Transport Victoria `bus-vehicle-positions` succeeded using exactly one
  `KeyID` contract. The response declared GTFS-Realtime 2.0/full-dataset,
  timestamped the feed at `2026-07-16T04:35:28Z`, and contained 1,595 vehicle
  entities. No private vehicle identifier or credential appeared in output.
- A separate no-network production-generation smoke returned the 09:07–09:31
  Flinders Street–Box Hill journey with Melbourne-offset timestamps.

## Final gates

| Gate | State | Evidence |
| --- | --- | --- |
| Specs and artifact consistency | passed | independent final adversarial review found no remaining implementation blocker |
| Focused package/command regressions | passed | GTFS, router, PTV, GTFS-R, and command suites |
| `mise run audit-check` | passed | format, build, vet, test, race, timezone, static, and Windows tasks under pinned Go 1.25.12 |
| Race detector | passed | `go test -race -count=1 ./...` |
| Host timezone independence | passed | full suite under UTC and Australia/Perth |
| Portable builds | passed | native, `CGO_ENABLED=0`, and Windows amd64 cross-build |
| JSON and request-count contracts | passed | goldens and local official-shape fixtures |
| Owner-authorized live PTV/GTFS-R probes | passed | explicit `--env-file .env`; file not inspected and values not displayed; details above |
| Production performance | passed | ten raw samples above; both budgets and 10x target met |
| Versioned release-candidate dogfood | passed | v0.5.0 broad matrix plus repaired fare, route/stop, and sequence contracts; no remaining release blocker |
