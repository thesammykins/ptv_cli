# AGENTS.md

Guidance for AI agents and contributors working in this repository.

## What this is

`ptv` is a Go CLI for Victorian public transport — a terminal equivalent of
Google Maps / Transit, scoped to VIC PTV. It combines:

- the **PTV Timetable API v3** (real-time departures, disruptions, lines,
  stations) — signed HMAC-SHA1 requests; and
- the **PTV GTFS static feed** (ingested into local SQLite) for offline,
  multi-modal journey planning via the Connection Scan Algorithm (CSA).

## Build, vet, test

```sh
go build ./...     # build everything
go vet ./...       # static checks
go test ./...      # unit tests
go build -o /tmp/ptv .   # produce a runnable binary for live testing
```

There is no separate linter config; keep `go vet` clean and run `gofmt -w` on
any files you touch. Pure-Go SQLite (`modernc.org/sqlite`) is used, so
**`CGO_ENABLED=0`** works and cross-compilation needs no C toolchain.

## Layout

```
main.go              entrypoint; injects build metadata (version/commit/date)
cmd/                 cobra commands (one file per command/area)
  root.go            root command, global --json/--limit flags, version
  auth.go            credential capture/verify (OS keyring)
  plan.go            CSA journey planner (+ geocoding, disruptions, freshness)
  plan_disruptions.go disruption→journey matching
  mode.go            ptv tram/bus/vline (per-mode route view + lines/next)
  next.go            real-time departures from a stop
  lines/stops/station/disruptions/fare/outlets/search.go  lookups
  gtfs.go            gtfs update/status/check (local dataset management)
  resolve.go         resolveRoute / resolveStop helpers
  helpers.go         printJSON, routeTypeName, parseMode, joinArgs, ...
internal/
  config/            credential resolution (env → keyring → .env), paths
  credstore/         cross-platform OS keyring wrapper (go-keyring)
  geocode/           OpenStreetMap Nominatim client (VIC-biased, cached)
  ptvapi/            HMAC-SHA1 signer, HTTP client, typed v3 models, endpoints
  gtfsrt/            Transport Victoria Open Data GTFS Realtime protobuf client
  gtfs/              downloader, zip-of-zips ingest, schema/queries, freshness
  router/            Connection Scan Algorithm (earliest-arrival + arrive-by)
  model/             shared domain types (Stop, Connection, Journey, Leg, ...)
  render/            table output helper
```

## Conventions

- **Commands are cobra commands** registered via `rootCmd.AddCommand` in each
  file's `init()`. Follow the existing structure when adding one.
- **`--json` everywhere.** Every command must honour the global `flagJSON`
  (use `printJSON`). JSON is for scripting/agents — keep it on **stdout** and
  send human warnings/notes to **stderr** so JSON stays parseable.
- **Time zone is `Australia/Melbourne`** for all user-facing times; the
  Timetable API speaks UTC and is converted for display.
- **Credentials**: PTV Timetable API credentials are resolved env → OS keyring →
  explicit `--env-file`; the CLI does not auto-read `.env`. Never commit `.env`
  or secrets. Prefer `ptv auth login` (keyring). The API signature is
  `HMAC-SHA1("{path}?{query-incl-devid}")`, uppercase hex, appended as
  `&signature=`. Optional Transport Victoria Open Data GTFS-R uses
  `PTV_OPENDATA_KEY_ID` (`PTV_OPENDATA_KEYID` alias accepted) for the
  subscription key, optionally `PTV_OPENDATA_API_ID` for the data platform token,
  and is not stored by `ptv auth`.

## Gotchas (read before editing)

- **GTFS `route_id` ≠ API `route_id`.** They are different namespaces. Match
  disruptions to journey legs by normalised route **name/number**, not id (see
  `cmd/plan_disruptions.go`). GTFS ids are namespaced `{feedMode}:{id}`.
- **GTFS `feed_mode`** (the inner-zip number) is the reliable mode indicator:
  1=V/Line Train, 2=Metro Train, 3=Tram, 4=Bus, 5=V/Line Coach, 6=Regional Bus
  (others=bus). `Leg.RouteType` carries this feed_mode; map to API route_type
  via `feedToAPIType` and to a label via `gtfsModeName`.
- **Search terms can't contain `/`.** PTV signs the URL path, and a `/` breaks
  the signature (403). `ptvapi.Search` collapses `/` to spaces — many stop
  names use it (e.g. "Bourke St/Spencer St").
- **Negative latitudes need a `--` separator** on the CLI, because cobra treats
  a leading `-` as a flag (e.g. `ptv plan --arrive-by 09:00 -- "-37.81,144.96" "Camberwell"`).
- **`plan` requires credentials** even though planning is local
  (`config.Load()` errors without them). Disruption + freshness checks are
  best-effort and must never block planning.
- **GTFS data is a rolling ~30-day window**, published ~weekly. Past the window
  the planner silently finds no services. Freshness checks (below) warn but do
  **not** block.
- **Vehicle lookup is descriptor-driven, not a direct fleet API.** PTV exposes
  `vehicle_descriptor` and `vehicle_position` only when expanded on some
  departures/runs. There is no public direct "find vehicle by id" endpoint.
  Metro train descriptors appear as consist strings (e.g.
  `113M-114M-1357T-1422T-243M-244M`) and can be matched by any component; observed
  descriptions from an exhaustive train stop scan are `3 Car Silver Hitachi`,
  `3 Car Xtrapolis`, `6 Car Comeng`, `6 Car Siemens`, `6 Car Xtrapolis`.
  Tram descriptors can appear from some stop departure contexts (`Yarra Trams`,
  numeric ids) but route-filtered scans may not expose them. Bus often exposes
  position without an identifying descriptor; V/Line descriptors were not
  observed in broad sampling. GTFS-R vehicle-position feeds can expose live
  train/tram/bus/VLine vehicle ids and positions; train labels may be consist
  strings and should match by component. Keep user messages explicit about
  `last_spotted` vs current service and about external fleet sources being
  existence/class references, not proof of live PTV visibility.
- **Transport Victoria GTFS Realtime is separate from the Timetable API.** It is
  protobuf, uses the Open Data subscription key as a `KeyId`/`KeyID` header plus
  optional `PTV_OPENDATA_API_ID` bearer token, while some feeds prefer
  `Ocp-Apim-Subscription-Key`. It has feeds for trip updates, service alerts and
  vehicle positions across train/tram/bus/VLine. Live testing returned 401 for
  all unauthenticated feed requests. Use `ptv gtfs realtime` to list or inspect
  the feed catalog. `ptv vehicle` uses vehicle-position feeds for optional
  enrichment and direct vehicle lookup because PTV Timetable API descriptors are
  frequently absent outside Metro trains and some trams.

## GTFS freshness

`internal/gtfs/freshness.go` provides staleness + upstream-update detection:

- Feed provenance (`ETag`/`Last-Modified`/size) is captured on `gtfs update`
  and stored in the `meta` table.
- `Freshness(ctx, store, url, allowNetwork, force)` returns a `FreshnessReport`
  (age, `Stale` vs a 7-day threshold — override with `PTV_GTFS_STALE_DAYS`,
  and `UpdateAvailable`). The live HEAD check is **throttled to once / 24h**
  (cached in `meta`); `force` bypasses the throttle.
- `plan` prints warnings to stderr (suppress with `--no-update-check`);
  `ptv gtfs status` shows the report; `ptv gtfs check` forces a live check.

## Releases

Tagging `vX.Y.Z` triggers `.github/workflows/release.yml`, which runs
GoReleaser (`.goreleaser.yaml`) to cross-compile for linux/macOS/windows ×
amd64/arm64 (`CGO_ENABLED=0`) and publish archives + `checksums.txt`.
`version`/`commit`/`date` are stamped via ldflags and shown by `ptv version`.
`.github/workflows/ci.yml` runs build/vet/test on push and PRs.

## Adding a command (checklist)

1. New file in `cmd/`; define a `*cobra.Command` with a clear `Use`/`Short`.
2. Register it in that file's `init()` via `rootCmd.AddCommand` (or a parent).
3. Support `--json` (use `printJSON`); human output via `render.NewTable`.
4. Reuse `resolveRoute`/`resolveStop`/`loadClient`/`ctx` and the helpers.
5. Run `gofmt -w`, `go build/vet/test`, and live-test the binary.
