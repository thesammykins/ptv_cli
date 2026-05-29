# ptv — Victorian public transport from your terminal

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
- 🚋 Mode-scoped commands `ptv tram`, `ptv bus`, `ptv vline` for a full
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

### Build from source

```sh
go build -o ptv .
# optionally: mv ptv /usr/local/bin/
```

Requires Go 1.25+. The journey planner uses a local SQLite database
(`modernc.org/sqlite`, pure Go — no cgo).

## Credentials

You need a PTV Timetable API **key** and **user/dev id**. Request them from PTV
(see the [PTV API access page](https://www.ptv.vic.gov.au/footer/data-and-reporting/datasets/ptv-timetable-api/)).

Credentials are resolved in this order:

1. Environment variables `PTV_API_KEY` and `PTV_API_USERID`.
2. The OS keyring (macOS Keychain, Windows Credential Manager, Linux Secret
   Service) — populated via `ptv auth login`.

For local development only, pass `--env-file <path>` to load a dotenv file
explicitly. The CLI does not auto-read `.env` from the working directory.

Store them securely in the OS keyring:

```sh
ptv auth login      # prompts, verifies against the API, stores in the keyring
ptv auth status     # shows where credentials are being read from
ptv auth check      # makes a signed test call
ptv auth logout     # removes credentials from the keyring
```

> The signature scheme is HMAC-SHA1 over `"{path}?{query-incl-devid}"`, keyed
> by the API key; the uppercase-hex result is appended as `&signature=`.

## Journey planning data (GTFS)

Trip planning needs the PTV GTFS static feed ingested into a local database:

```sh
ptv gtfs update     # downloads (~210 MB) and ingests into SQLite
ptv gtfs status     # shows ingest time, data age/staleness and row counts
ptv gtfs check      # asks the endpoint whether a newer feed is available
```

The feed is a zip-of-zips (one feed per mode). Stops and routes are namespaced
`{feedMode}:{id}` to avoid cross-feed collisions, and proximity walk-transfers
(≤250 m) are generated to connect stops across modes for multi-modal routing.

### Keeping the data fresh

PTV publishes the GTFS feed roughly **weekly**, and each export only contains a
**rolling ~30 days** of timetable data. If your local copy falls outside that
window, the planner will simply find no services for the requested date — so it
pays to stay current.

`ptv` helps in two ways:

- **Staleness warning** — if the local data is older than **7 days** (override
  with `PTV_GTFS_STALE_DAYS`), `ptv plan` prints a warning to stderr.
- **Upstream update detection** — `ptv` records the feed's `ETag`/`Last-Modified`
  on ingest and compares them against the endpoint with a cheap `HEAD` request,
  **throttled to once per 24h**. `ptv plan` flags when a newer feed is available;
  `ptv gtfs check` forces an immediate check.

Both checks are **non-blocking** (they only warn) and run during `ptv plan`
unless you pass `--no-update-check`. Warnings go to **stderr**, so `--json`
output on stdout stays clean for scripts and agents.

## Usage

```text
ptv auth        Manage and verify API credentials
ptv search      Search stops, routes and outlets
ptv lines       List transport lines/routes (lines show <route> for detail)
ptv stops       Find stops near a location (stops near) or on a route (stops on)
ptv station     Show facilities and platforms for a station/stop
ptv next        How soon the next services depart a stop (real-time)
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

`ptv tram`, `ptv bus` and `ptv vline` give an ergonomic, per-mode entry point:

```sh
ptv tram 109                 # route header, directions, ordered stops + disruptions
ptv tram lines               # list every tram route
ptv tram next "Melbourne University"   # live departures from a stop (tram only)

ptv bus 246
ptv vline 1745                # Geelong - Melbourne
```

Each accepts `--json` for structured output.

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
  ptvapi/     HMAC-SHA1 signer, HTTP client, typed v3 responses, endpoints
  gtfs/       downloader, zip-of-zips ingest, SQLite schema/queries, timetable
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

Every command accepts `--json`, emitting stable, structured output suitable for
scripts and AI agents. Examples:

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

Tagging a commit `vX.Y.Z` triggers the **Release** GitHub Action, which first
runs `go build ./...`, `go vet ./...`, and `go test ./...`, then runs
[GoReleaser](https://goreleaser.com/) to cross-compile `ptv` for
linux/macOS/windows × amd64/arm64 (pure-Go, `CGO_ENABLED=0`) and attaches the
archives + `checksums.txt` to a GitHub Release. The binary's `version`,
`commit` and build `date` are stamped via ldflags and surfaced by
`ptv version`.

```sh
git tag v0.1.0
git push origin v0.1.0   # → builds and publishes the release
```

A lightweight **CI** workflow runs `go build`/`go vet`/`go test` on pushes to
`main` and pull requests.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

Tests cover the HMAC signer, the CSA planner (earliest-arrival,
latest-departure and the no-journey case) on a fixture network, and GTFS id
parsing.

## Notes

- Never commit `.env` or credentials. Prefer `ptv auth login` (keyring), or use
  `--env-file <path>` only when you intentionally need dotenv-based local setup.
- The GTFS download is large; ingest once and refresh on demand.
- There is **no journey-planning endpoint** in the PTV API — `ptv plan` is
  built entirely from the GTFS feed, optionally surfacing real-time data via
  `ptv next`.
