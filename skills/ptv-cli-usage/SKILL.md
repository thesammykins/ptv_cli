---
name: ptv-cli-usage
description: Practical usage guide for running the production ptv CLI as an end user. Use when an agent needs to exercise commands, validate examples, run live PTV API workflows, query stops/routes/departures/disruptions/fares/outlets, use GTFS planning, or produce reliable command examples for ptv_cli.
---

# PTV CLI Usage

Use this skill when interacting with the production `ptv` CLI. Assume the user has installed `ptv`, it is available on `PATH`, credentials are configured, and GTFS data is already available for planning.

## Core Rules

- Do not read or print secrets. Let credentials resolve normally from env or OS keyring. Use `--env-file` only when explicitly requested.
- Keep JSON stdout clean. If testing `--json`, parse stdout and treat stderr warnings separately.
- Use human-logical inputs first: public route numbers, route names, station names, and addresses. Use API IDs only to disambiguate.
- For negative Melbourne latitudes on the command line, put coordinates after `--`.

## Baseline

Run a baseline check:

```sh
ptv version
ptv auth status
ptv auth check
```

If planning fails because GTFS data is unavailable, tell the user to run the normal end-user setup command:

```sh
ptv gtfs update
```

Do not introduce temporary data directories for ordinary CLI usage. Use the same commands an end user would run.

## Command Families

### Search

```sh
ptv search 'Flinders Street' --limit 5
ptv search 'Bourke St/Spencer St' --json --limit 3
```

Slashes in search terms are supported by collapsing `/` before signing API requests.

### Stops And Stations

Use the user's actual address/place when provided. Otherwise use generic placeholders in examples rather than hard-coded personal addresses.

```sh
ptv stops near '<address or place>' --limit 8
ptv stops near -- '-37.818,144.952' --limit 8
ptv stops on 'Mernda' --mode train
ptv station 'Flinders Street'
ptv station 1071
```

`station` should prefer train stations for ambiguous station-like names unless `--mode` is supplied.

### Lines And Mode Commands

```sh
ptv lines --mode train --limit 5
ptv lines 'Mernda'
ptv lines show 'Mernda'
ptv tram 109
ptv tram lines --limit 5
ptv bus 382
ptv vline 1745
```

Use route numbers for tram/bus commands. Use route IDs only when a route name is ambiguous, such as V/Line Geelong-related services.

### Departures

```sh
ptv next 'Flinders Street' --mode train --limit 5
ptv next 'Melbourne University' --mode tram --route 109 --limit 3
ptv tram next 'Melbourne University' --limit 3
```

When filtering by `--route` with a `--mode`, public route numbers should work.

### Vehicles

```sh
ptv vehicle 243M --stop Mordialloc --route Frankston
ptv vehicle 6059 --stop 'Melbourne Central Station'
ptv vehicles BS11ZU --stop Chadstone
ptv vehicle '17-903--1-Sun12-903738' --stop 11293 --json
```

`vehicle` and `vehicles` are aliases. Train and tram lookups can use PTV
Timetable API `vehicle_descriptor` context, but GTFS Realtime is often the
better source for physical vehicle identity and position. Train, tram, bus and
V/Line physical-id lookup uses Transport Victoria GTFS Realtime vehicle-position
feeds when Open Data credentials are configured with `ptv auth opendata login`,
`PTV_OPENDATA_KEY_ID`, and `PTV_OPENDATA_API_ID` if the account requires the
platform bearer token.

### GTFS Realtime

```sh
ptv gtfs realtime
ptv gtfs realtime bus-vehicle-positions --json
ptv gtfs realtime --all
```

The catalog command does not require Open Data credentials. Fetching one or all
feeds may be tried without credentials, but live testing currently returns 401;
configure `PTV_OPENDATA_KEY_ID`, and `PTV_OPENDATA_API_ID` if the account
requires the platform bearer token. Use this command to verify feed timestamps
and entity counts before debugging higher-level live data behavior. `ptv vehicle`
uses vehicle-position feeds today; `next`, `plan`, and `disruptions` do not yet
merge GTFS-R trip updates or service alerts.

### Planning

Planning commands should work directly with the user's installed CLI data. Replace placeholders with the user's actual places when known.

```sh
ptv plan '<origin address or place>' 'Flinders Street Station' --no-update-check
ptv plan '<origin address or place>' 'Box Hill Station' --arrive-by 17:00 --no-update-check
ptv plan 'Werribee Station' 'Belgrave Station' --depart 09:30 --no-update-check
ptv plan --json '<origin address or place>' 'Geelong Station' --depart 10:00 --no-update-check
```

Planning requires credentials by product design, even though routing uses local GTFS. Disruption and freshness checks are best-effort unless explicitly disabled.

### Disruptions, Outlets, Fare

```sh
ptv disruptions --mode train --limit 5
ptv disruptions --json --mode tram --limit 2
ptv outlets 'Flinders Street' --limit 5
ptv outlets --json 'Flinders Street' --limit 5
ptv fare --min-zone 1 --max-zone 2
ptv fare --json --min-zone 1 --max-zone 2
```

Fare output may reflect upstream API behavior; report zero-fare responses rather than silently assuming they are correct.

## Validation Checklist

When checking behavior, verify:

- command exits successfully or returns a useful actionable error
- `--limit` limits visible rows and relevant JSON arrays
- `--json` emits valid JSON on stdout
- human output is readable and sorted logically
- route/station resolution chooses expected public-facing entities
- route details do not begin with confusing unordered `SEQ 0` blocks
- planning includes plausible transfers, times, and disruption annotations

## Reporting

For issues, include:

- exact command run
- exit status
- relevant stdout/stderr excerpt
- expected behavior
- actual behavior
- whether this is command UX, resolver behavior, live API data, docs, or tests

For successful validation, summarize the command matrix and mention whether planning worked with the user's existing GTFS data.
