---
name: ptv-cli-usage
description: Run and validate the installed production ptv CLI for Victorian public transport. Use when checking end-user commands, live PTV Timetable or Transport Victoria GTFS Realtime behavior, stop/route/departure/disruption/fare/outlet queries, local GTFS planning, JSON contracts, or reproducible ptv_cli workflow reports.
---

# PTV CLI usage

Exercise the installed `ptv` binary as an end user. When the task concerns
source changes instead, build a separate binary and state that it is not a
released artifact.

## Safety and output rules

- Never read, print, log, or copy credentials or signed request URLs.
- Resolve credentials normally from environment or keyring. Use `--env-file`
  only when the user explicitly authorizes that file; keep the flag before the
  subcommand.
- Keep JSON stdout separate from stderr warnings and validate it with a JSON
  parser.
- Prefer public route numbers/names and station names. Use internal IDs only to
  disambiguate, and keep PTV run references, static-GTFS trip IDs, GTFS-R entity
  IDs, and vehicle labels distinct.
- Put negative coordinates after `--` so Cobra does not parse them as flags.

## Establish prerequisites

Start with:

```sh
ptv version
ptv auth status
```

Run `ptv auth check` only when a live Timetable API check is in scope. Open Data
feeds use separate credentials; inspect them with:

```sh
ptv auth opendata status
ptv auth opendata check
```

Static planning needs an installed GTFS database. Inspect it with `ptv gtfs
status --no-update-check`; run `ptv gtfs update` only when downloading and
replacing local data is in scope. Older published releases may still require
Timetable API credentials during plan startup even when network overlays are
disabled; report the observed version-specific behavior, not an inherent GTFS
requirement.

## Command matrix

### Search, stops, and stations

```sh
ptv search 'Flinders Street' --limit 5
ptv search 'Bourke St/Spencer St' --json --limit 3
ptv stops near '<address or place>' --limit 8
ptv stops near --limit 8 -- '-37.818,144.952'
ptv stops on 'Mernda' --mode train
ptv station 'Flinders Street'
ptv station 1071 --mode train
```

Use generic places in examples unless the user supplied an address. `station`
should prefer a train station for an ambiguous station-like name unless
`--mode` is supplied.

### Lines and mode commands

```sh
ptv lines --mode train --limit 5
ptv lines 'Mernda'
ptv lines show 'Mernda'
ptv train 'Mernda'
ptv tram 109
ptv tram lines --limit 5
ptv bus 382
ptv vline 1745
```

Use route numbers for tram and bus. Use an API route ID only after displaying
ambiguity choices to the user.

### Departures and vehicles

```sh
ptv next 'Flinders Street' --mode train --limit 5
ptv next 'Box Hill Central' --mode tram --route 109 --limit 3
ptv tram next 'Melbourne University' --limit 3
ptv vehicle 243M --stop Mordialloc --route Frankston
ptv vehicles 6059 --stop 'Melbourne Central Station'
ptv vehicle '<PTV run_ref>' --stop 11293 --json
```

`vehicle` and `vehicles` are aliases. PTV v3 vehicle descriptors are
context-dependent; GTFS-R vehicle-position feeds are often the better physical
identity/position source when Open Data credentials are configured. Do not
equate a PTV `run_ref` with a static-GTFS `trip_id` or GTFS-R entity ID.

### GTFS Realtime

```sh
ptv gtfs realtime
ptv gtfs realtime bus-vehicle-positions --json
ptv gtfs realtime --all
```

The catalog is local. Fetching feeds requires the Transport Victoria Open Data
subscription credential managed by `ptv auth opendata`; do not invent header
or bearer-token requirements beyond the current official per-feed contract.
Inspect feed timestamps and entity counts before claiming a vehicle observation
is current. `next`, `plan`, and `disruptions` do not currently merge GTFS-R trip
updates or service alerts.

### Planning

```sh
ptv plan '<origin>' 'Flinders Street Station' --no-update-check
ptv plan '<origin>' 'Box Hill Station' --arrive-by 17:00 --no-update-check
ptv plan 'Werribee Station' 'Belgrave Station' --depart 09:30 --no-update-check
ptv plan --json '<origin>' 'Geelong Station' --depart 10:00 --no-update-check
```

Use `--no-geocode` when both inputs are known stops/stations and the task is to
isolate local routing. Use `--no-disruptions` and `--no-update-check` only when
the requested check does not need those network overlays.

### Disruptions, outlets, and fare

```sh
ptv disruptions --mode train --limit 5
ptv disruptions --json --mode tram --limit 2
ptv outlets 'Flinders Street' --limit 5
ptv fare --min-zone 1 --max-zone 2
ptv fare --json --min-zone 1 --max-zone 2
```

Report suspicious upstream values, such as an all-zero fare response, without
silently replacing them with assumptions.

## Validate and report

For each workflow, record the binary version, exact command with secrets
redacted, exit status, whether stdout parsed as JSON, relevant stderr, expected
behavior, and actual behavior. Classify failures as command UX, resolution,
local GTFS/routing, upstream data/API, credentials, documentation, or tests.

Successful validation should name the command matrix exercised and the GTFS
generation/freshness used for planning. Never claim a live state from a stale or
undated observation.
