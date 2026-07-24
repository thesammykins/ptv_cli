# ptv — Victorian public transport from your terminal

`ptv` is an independent project and is not affiliated with or endorsed by
Public Transport Victoria.

`ptv` is a command-line companion for Victorian public transport. It combines
the **PTV Timetable API v3** (real-time departures, disruptions, line and
station information) with the **PTV GTFS static feed** (multi-modal journey
planning) to bring Google-Maps / Transit-app style functionality to the
terminal — exclusively for VIC PTV.

## Features

- 🔎 Search stops, routes (lines) and myki outlets.
- 🚆 Inspect train / tram / bus / V/Line lines, their directions and stops.
- 🏟️ Show station facilities, platforms and accessibility info.
- ⏱️ See how soon the next services depart a stop, with live countdowns,
  delays, platforms and disruptions (`ptv next`).
- 🗺️ Plan multi-modal A→B journeys (train + tram + bus + walking transfers)
  with **earliest-arrival** (`--depart`) and **latest-departure**
  (`--arrive-by`) modes (`ptv plan`).
- 📍 Enter **place names and addresses** (e.g. "Federation Square", "121
  Exhibition St, Melbourne") — geocoded via OpenStreetMap Nominatim — instead
  of `lat,lng`.
- ⚠️ Real-time **disruption flagging** inside `ptv plan`: any line your journey
  uses is checked for active disruptions and flagged inline.
- 🚉 Best-effort live vehicle lookup (`ptv vehicle`) from PTV vehicle
  descriptors/positions where the API exposes them, with optional Transport
  Victoria GTFS Realtime train, tram, bus and V/Line enrichment.
- 🚋 Mode-scoped commands `ptv train`, `ptv tram`, `ptv bus`, `ptv vline` for a full
  per-route view plus `lines` and `next` subcommands.
- 🔐 Cross-platform secure credential storage in the OS keyring.
- `--json` output on every command for scripting and AI agents.

## Installation

### Download a release (recommended)

Prebuilt binaries for Linux, macOS and Windows (amd64 + arm64) are attached to
each [GitHub Release](https://github.com/thesammykins/ptv_cli/releases).
Download the archive for your platform, extract `ptv`, and put it on your
`PATH`:

```sh
# example (macOS arm64)
tar -xzf ptv_*_Darwin_arm64.tar.gz
sudo mv ptv /usr/local/bin/
ptv version
```

macOS release binaries are not yet Developer ID signed/notarized. If Gatekeeper
blocks a downloaded `ptv` binary, remove the quarantine attribute after you have
verified the release archive/checksum:

```sh
xattr -d com.apple.quarantine /usr/local/bin/ptv
```

### Build from source

```sh
go build -o ptv .
# optionally: mv ptv /usr/local/bin/
```

Requires Go 1.25+. The journey planner uses a local SQLite database
(`modernc.org/sqlite`, pure Go — no cgo).

## Credentials

Networked Timetable API commands need a PTV Timetable API **key** and
**user/dev id**. Request them from PTV (see the
[PTV API access page](https://www.ptv.vic.gov.au/footer/data-and-reporting/datasets/ptv-timetable-api/)).
Local static-data commands (`gtfs status`, `gtfs check`, and the core `plan`
calculation) do not require Timetable credentials. `plan` uses them only for
its optional disruption overlay; without credentials it still returns the
local journey and reports that the overlay was skipped.

Credentials are resolved in this order:

1. Environment variables `PTV_API_KEY` and `PTV_API_USERID`.
2. The OS keyring (macOS Keychain, Windows Credential Manager, Linux Secret
   Service) — populated via `ptv auth login`.

For local development only, pass `--env-file <path>` to load a dotenv file
explicitly. The CLI does not auto-read `.env` from the working directory.

Optional Transport Victoria Open Data features use separate credentials from
the PTV Timetable API. Create an account at
<https://opendata.transport.vic.gov.au/>, subscribe to the public transport data,
then set the Open Data subscription key as `PTV_OPENDATA_KEY_ID` in the
environment or in an explicit `--env-file`; `PTV_OPENDATA_KEYID` is accepted as
an alias. `PTV_OPENDATA_API_ID` remains readable for compatibility with older
configuration, but the current feed contract does not document it and the CLI
does not transmit it. Without the subscription key, the core PTV
Timetable API features still work; commands that can use GTFS Realtime data will
print a warning and skip that enrichment.

The CLI also ships a reviewed, normalized snapshot of selected low-volatility
PTV Timetable API v3 enrichment data, limited to outlet records and explicitly
seeded station facilities. It never supplies static route or direction data:
live v3 or local GTFS remains authoritative for route topology, schedules and
journey planning, while GTFS-Realtime remains authoritative for live state.
See [the snapshot workflow](docs/ptv-v3-snapshot.md) for regeneration,
attribution, and the monthly update process.

Store them securely in the OS keyring:

```sh
ptv auth login      # prompts, verifies against the API, stores in the keyring
ptv auth status     # shows where credentials are being read from
ptv auth check      # makes a signed test call
ptv auth logout     # removes credentials from the keyring
```

Open Data credentials can also be stored in the OS keyring, independently from
the PTV Timetable API credentials:

```sh
ptv auth opendata login    # prompts for Open Data key/token, verifies GTFS-R
ptv auth opendata status   # shows whether Open Data credentials are configured
ptv auth opendata check    # makes a GTFS Realtime test request
ptv auth opendata logout   # removes stored Open Data credentials
```

### What works without a v3 API key?

The v3 key is optional for the local timetable and planner. After `ptv gtfs
update`, the CLI can search, inspect lines/stops/stations, show scheduled
departures, calculate fares, and plan journeys entirely from the versioned GTFS
static database. It does not silently replace that authoritative route topology
or timetable with a partial API response.

The separate Transport Victoria Open Data key can add real-time data without a
v3 key. These credentials are independent: an Open Data key does not unlock v3
platforms or v3 endpoint data, and a v3 key does not unlock GTFS-Realtime feeds.

| Capability | No v3 key | v3 key configured |
| --- | --- | --- |
| Stops, routes, lines, timetable/trip queries and `ptv plan` | Local GTFS static data; available after `ptv gtfs update` | Same GTFS source, with optional v3 enrichment where supported |
| Search and outlets | GTFS stops/routes plus the reviewed low-volatility outlet snapshot | GTFS stops/routes plus live v3 outlet results when available |
| Station details | GTFS station/parent-platform semantics plus seeded snapshot facilities when matched | Live v3 facilities and route/platform enrichment, with static fallback |
| `ptv next` | Scheduled GTFS departures; with an Open Data key, GTFS-R delays, estimates and skips can be applied | Same, plus best-effort v3 platform/run/disruption enrichment |
| `ptv disruptions` | Open Data service alerts for the catalogued train/tram feeds when that key is configured; otherwise an explicit unavailable result | Open Data alerts plus v3 bus/V/Line enrichment and v3 route filtering when available |
| `ptv vehicle` | GTFS-R vehicle positions for train, tram, bus and V/Line when the Open Data key is configured; otherwise no live vehicle feed | Adds v3 stop/run/descriptor context where PTV exposes it |
| Journey disruption overlay | No v3 overlay; the local journey still works and reports the skipped overlay | Best-effort v3 disruption overlay on the local journey |

Without either credential set, the CLI remains useful but is schedule-only for
live-sensitive commands: `next` labels the result as scheduled, `vehicle`
cannot query live vehicle feeds, and `disruptions` reports that live alerts are
unavailable. Static snapshot data is supplementary metadata only; it does not
contain live route topology, current departures, current platforms, or a
replacement for the GTFS timetable.

Current live-source wiring:

- `ptv vehicle` uses GTFS-R vehicle-position feeds for all four mode groups when
  Open Data credentials are configured, and can find vehicles even when v3 has no
  `vehicle_descriptor`.
- `ptv next` uses GTFS-R trip updates for estimates/delays/skips when the
  matching Open Data feed is available, then falls back to scheduled GTFS times.
- `ptv disruptions` merges Open Data service alerts and optional v3 enrichment;
  `ptv plan` remains locally planned and only its optional disruption overlay
  uses v3 today.
- `ptv gtfs realtime` lists and inspects the official GTFS-R trip update,
  service alert and vehicle-position feeds for debugging or scripting.

> The signature scheme is HMAC-SHA1 over `"{path}?{query-incl-devid}"`, keyed
> by the API key; the uppercase-hex result is appended as `&signature=`.

## Journey planning data (GTFS)

Trip planning needs the PTV GTFS static feed ingested into a local database.
No Timetable API credentials are needed for this lifecycle:

```sh
ptv gtfs update     # downloads (~210 MB) and ingests into SQLite
ptv gtfs status     # shows ingest time, data age/staleness and row counts
ptv gtfs check      # asks the endpoint whether a newer feed is available
ptv gtfs realtime   # list Transport Victoria GTFS Realtime feeds
```

The feed is a zip-of-zips (one feed per mode). Source identities are kept in
their own feed namespace and compiled into generation-local integer keys for
routing. Explicit station hierarchy, pathways, transfer restrictions,
pickup/drop-off rules, and linked-trip continuations are retained. Conservative
proximity transfers (≤250 m) connect otherwise unrelated stops only where no
explicit rule makes that inference unsafe.

An update is built in a private staging database and is published only after
required files, source relationships, counts, coverage, indexes, and database
integrity validate. Publication atomically changes a small manifest to select
an immutable generation; a failed or interrupted update leaves the previous
generation usable. Databases created by the earlier fixed-file layout require
one `ptv gtfs update` re-ingest.

### Keeping the data fresh

PTV publishes the GTFS feed roughly **weekly**, and each export only contains a
bounded rolling timetable window. The CLI validates the requested service date
against the generation's recorded coverage instead of silently treating an
out-of-window query as an ordinary no-journey result.

Freshness is reported as `current`, `changed`, `stale`, or `unknown` using the
source URL, service coverage, publication/ingest evidence, and upstream
validators:

- **Stale** means service coverage excludes the relevant date or the best
  available publication-time evidence exceeds the configured threshold
  (`PTV_GTFS_STALE_DAYS`, seven days by default).
- **Changed** means a successful upstream check returned a different comparable
  `ETag` or `Last-Modified` validator.
- **Unknown** is explicit when a check fails or comparable validators are
  absent; missing evidence is never reported as current.
- Successful automatic checks are throttled to once per 24 hours. Failed
  automatic checks use persisted backoff; `ptv gtfs check --force` bypasses it.

Both checks are **non-blocking** (they only warn) and run during `ptv plan`
unless you pass `--no-update-check`. Warnings go to **stderr**, so `--json`
output on stdout stays clean for scripts and agents.

### GTFS Realtime feeds

Transport Victoria Open Data also publishes GTFS Realtime protobuf feeds for
trip updates, vehicle positions and service alerts. Live testing from this repo
currently returns `401` for unauthenticated feed requests, so create an account
at <https://opendata.transport.vic.gov.au/> and configure `PTV_OPENDATA_KEY_ID`
before fetching. Use `ptv gtfs realtime` to list the known feed catalog, or
fetch one/all feeds when
the Open Data subscription key is configured. Each feed request sends exactly
one documented `KeyID` header; the compatibility API-ID value is not sent:

```sh
ptv gtfs realtime
ptv --env-file .env gtfs realtime bus-vehicle-positions --json
ptv --env-file .env gtfs realtime --all
```

The command reports feed timestamps and entity counts. `ptv vehicle` uses the
vehicle-position feeds, and `ptv next`/`ptv disruptions` consume the applicable
GTFS-R trip-update/service-alert feeds when their Open Data key is configured.
`ptv plan` remains a local GTFS calculation; its optional live disruption
overlay is supplied by the v3 API rather than GTFS-R.

## Usage

```text
ptv auth        Manage and verify API credentials
ptv search      Search stops, routes and outlets
ptv lines       List transport lines/routes (lines show <route> for detail)
ptv stops       Find stops near a location (stops near) or on a route (stops on)
ptv station     Show facilities and platforms for a station/stop
ptv next        How soon the next services depart a stop (real-time)
ptv vehicle     Best available live information for a vehicle id or run_ref
ptv train       Metro train route info, stops, departures and disruptions
ptv tram        Tram route info, stops, departures and disruptions
ptv bus         Bus route info, stops, departures and disruptions
ptv vline       V/Line route info, stops, departures and disruptions
ptv plan        Plan a multi-modal journey between two places
ptv disruptions View current and planned service disruptions
ptv fare        Estimate a myki fare by zone
ptv outlets     List myki ticket outlets
ptv gtfs        Manage the local GTFS dataset used for journey planning
ptv version     Print version and build information
```

Global flags: `--json`, `--limit`.

### Mode-scoped commands

`ptv train`, `ptv tram`, `ptv bus` and `ptv vline` give an ergonomic, per-mode
entry point:

```sh
ptv tram 109                 # route header, directions, known stop order + disruptions
ptv tram lines               # list every tram route
ptv tram next "Melbourne University"   # live departures from a stop (tram only)

ptv train 1
ptv bus 246
ptv vline 1745                # Geelong - Melbourne
```

Each accepts `--json` for structured output.

### Vehicle lookup

`ptv vehicle` (alias `ptv vehicles`) combines the PTV Timetable API with
Transport Victoria GTFS Realtime vehicle-position feeds. In practice GTFS-R is
often the superior source for physical vehicle identity and live position,
especially for bus and V/Line and for all-mode direct ID lookup. The Timetable
API does not provide a direct "find vehicle by id" endpoint, so the command first
uses v3 stop/run context when you provide hints, then uses GTFS-R
vehicle-position feeds for train/tram/bus/VLine matches when Open Data
credentials are configured.

```sh
# Search a Metro train car/consist component seen at a stop/line.
ptv vehicle 243M --stop Mordialloc --route Frankston

# Search a tram number from a stop context.
ptv vehicle 6059 --stop "Melbourne Central Station"

# Treat the argument as a PTV run_ref if no physical vehicle id is found.
ptv vehicle 952377 --json

# Enable optional GTFS Realtime enrichment from an explicit env file.
ptv --env-file .env vehicle '17-903--1-Sun12-903738' --stop 11293

# Search a physical bus id from the GTFS Realtime vehicle-position feed.
ptv --env-file .env vehicles BS11ZU --stop Chadstone

# Search a train consist component from the GTFS Realtime vehicle-position feed.
ptv --env-file .env vehicle 381M

# Slow fallback: scan a bounded number of active route runs.
ptv vehicle 243M --scan-routes 20
```

What PTV currently exposes in live output:

- Metro trains: `vehicle_descriptor.operator` is usually `Metro Trains
  Melbourne`; `vehicle_descriptor.id` is a consist string such as
  `113M-114M-1357T-1422T-243M-244M`; `vehicle_descriptor.description` includes
  values observed in an all-route/all-stop scan: `3 Car Silver Hitachi`, `3 Car
  Xtrapolis`, `6 Car Comeng`, `6 Car Siemens`, `6 Car Xtrapolis`.
- Trams: some stop departure contexts expose `vehicle_descriptor.operator` as
  `Yarra Trams` and a numeric `vehicle_descriptor.id` such as `6059`; the
  description is often blank and route-filtered scans may not expose it.
- GTFS Realtime: with `PTV_OPENDATA_KEY_ID`, `ptv vehicle` checks the official
  train, tram, bus and V/Line vehicle-position feeds for an exact public vehicle
  label (including a train consist label). PTV `run_ref`, static `trip_id`, and
  GTFS-R entity identifiers are never equated by matching strings. The private
  upstream vehicle ID is not emitted. GTFS-R position, occupancy, timestamps,
  and conservative freshness evidence remain visibly separate from PTV data.
- V/Line: no Timetable API `vehicle_descriptor` data was observed in broad live
  sampling, but GTFS-R vehicle positions can expose public V/Line labels.

The Transport Victoria Open Data endpoint is useful beyond vehicle enrichment: it
also publishes GTFS Realtime trip updates, service alerts and vehicle-position
feeds for Metro Train, Yarra Trams, Metro & Regional Bus, and V/Line. `ptv
vehicle` currently uses the vehicle-position feeds for all four mode groups;
`ptv gtfs realtime` exposes trip updates and alerts for inspection until they are
merged into higher-level commands.

Shortfalls:

- New, testing, commissioning, non-revenue or not-currently-running vehicles may
  exist in external fleet references but not appear in PTV live data.
- `last_spotted` means the vehicle appeared in earlier PTV departure data for
  the hinted stop/route and does not appear in upcoming departures there now.
- Use external fleet references to confirm that a vehicle exists or belongs to a
  set/class; use `ptv vehicle` only for what PTV currently exposes live.

### Examples

```sh
# Real-time departures
ptv next "Flinders Street" --mode train --limit 6

# Plan a trip leaving now
ptv plan "Flinders Street" "Box Hill"

# Use a place name or street address as origin/destination (geocoded)
ptv plan "Federation Square" "Melbourne Zoo"
ptv plan "121 Exhibition St, Melbourne" "Box Hill"

# Leave at a specific time
ptv plan "Flinders Street" "Southern Cross" --depart 17:30

# Arrive no later than a time, using coordinates as the origin.
# A leading '-' (Melbourne latitudes) must follow a '--' separator.
ptv plan --arrive-by 09:00 -- "-37.8183,144.9671" "Camberwell"

# Plan flags
#   --depart HH:MM | "YYYY-MM-DD HH:MM"   leave at (default: now)
#   --arrive-by HH:MM | "..."             arrive no later than
#   --date YYYY-MM-DD                     service date for HH:MM times
#   --radius <metres>                     search radius for lat,lng / geocoded points
#   --allow-conditional                   allow pickup/drop-off values requiring coordination
#   --no-geocode                          match local stop names only (no address lookup)
#   --no-disruptions                      skip the real-time disruption overlay
#   --no-update-check                     skip the GTFS staleness / upstream-update check
```

`<from>`/`<to>` accept a stop name (prefix/substring matched across all modes,
so a station's train, tram and bus stops are all considered), a `lat,lng`
coordinate, or a free-text **place / address** that is geocoded via
OpenStreetMap Nominatim (biased to Victoria). Local stop-name matches take
precedence; geocoding is the fallback. Use `--no-geocode` to disable it.

`ptv stops near` also accepts either `lat,lng` coordinates or a place/address,
for example `ptv stops near "36 McClelland Drive, Mill Park"`.

### Disruptions in `plan`

After planning, `ptv plan` checks every line your journey uses against the live
PTV disruptions feed. Affected legs are flagged with `⚠` and a **Disruptions**
section lists each active disruption (status, title and URL). With `--json`,
each affected leg carries `disrupted` / `disruption_ids` and the journey gains a
`disruptions` array. The overlay is best-effort — if it can't reach the API it
is skipped with a one-line note (the planner itself is fully local).

## Architecture

```
internal/
  config/     credential resolution (env → keyring; explicit --env-file), paths
  credstore/  cross-platform OS keyring wrapper (go-keyring)
  geocode/    OpenStreetMap Nominatim client (VIC-biased, cached, throttled)
  localtime/  embedded Australia/Melbourne service-day and display-time rules
  ptvapi/     HMAC-SHA1 signer, HTTP client, typed v3 responses, endpoints
  gtfs/       validated generation ingest, SQLite schema/queries, timetable
  router/     Connection Scan Algorithm (earliest-arrival + latest-departure)
  model/      shared domain types (Stop, Connection, Journey, Leg, ...)
  render/     table output helper
cmd/          cobra command definitions
```

The router uses the **Connection Scan Algorithm (CSA)**. Latest-departure
(`--arrive-by`) is implemented by running a forward scan over a time-reversed
connection set and flipping the resulting legs back to forward time.

Times are handled in `Australia/Melbourne`; the Timetable API uses UTC and is
converted for display.

## JSON output for agents

Every command accepts `--json`, emitting normalized structured output suitable
for scripts and AI agents. It is not a raw passthrough of upstream API models.
Times and identifier namespaces are explicit, warnings remain valid structured
fields where applicable, and diagnostic text goes to stderr so stdout is one
JSON document. See [the JSON contract](docs/json-contract.md) for the shared
rules and command shapes. Examples:

```sh
ptv plan "Federation Square" "Box Hill" --json   # legs[], disruptions[], per-leg disrupted/disruption_ids
ptv tram 109 --json                              # route, directions, stops, disruptions
ptv next "Flinders Street" --mode train --json
ptv gtfs status --json                           # counts + freshness{} report
ptv gtfs check --json                            # upstream update check
ptv gtfs update --json                           # ingest counts after update
ptv version --json
```

## Releases

The root [`VERSION`](VERSION) file records the next reviewed release version.
Source builds report that value with a `-dev` suffix. Tagging the merged commit
with the matching `vX.Y.Z` triggers the **Release** GitHub Action, which first
rejects a tag/version mismatch, runs `go build ./...`, `go vet ./...`, and
`go test ./...`, then runs
[GoReleaser](https://goreleaser.com/) to cross-compile `ptv` for
linux/macOS/windows × amd64/arm64 (pure-Go, `CGO_ENABLED=0`) and attaches the
archives + `checksums.txt` to a GitHub Release. The binary's `version`,
`commit` and build `date` are stamped via ldflags and surfaced by
`ptv version`.

```sh
version="$(tr -d '\r\n' < VERSION)"
git tag "v$version"
git push origin "v$version"   # → builds and publishes the release
```

A lightweight **CI** workflow runs `go build`/`go vet`/`go test` on pushes to
`main` and pull requests.

## Development

The checked-in `mise.toml` pins the supported Go patch release and exposes the
same local gates used by contributors. It does not load `.env`. After reviewing
the file, trust it once per checkout:

```sh
mise trust
mise install
mise run check
# Extended race, timezone and CGO-disabled verification:
mise run audit-check
```

Without `mise`, run the equivalent commands directly:

```sh
gofmt -l .  # must produce no output
go build ./...
go vet ./...
go test ./...
```

Tests cover official HTTP response shapes and request counts, identifier
namespace collisions, GTFS compilation and service-day semantics, transfer
precedence, forward and reverse CSA against an independent event-graph oracle,
freshness/backoff, cancellation, local-time behavior, generation publication,
and normalized JSON goldens.

## Notes

- Never commit `.env` or credentials. Prefer `ptv auth login` (keyring), or use
  `--env-file <path>` only when you intentionally need dotenv-based local setup.
- The GTFS download is large; ingest once and refresh on demand.
- There is **no journey-planning endpoint** in the PTV API — `ptv plan` is
  built entirely from the GTFS feed, optionally surfacing real-time data via
  `ptv next`.
