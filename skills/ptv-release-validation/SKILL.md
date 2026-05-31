---
name: ptv-release-validation
description: Release and production smoke-testing workflow for this Go PTV CLI. Use when validating a pushed tag/release, downloading production binaries from GitHub Releases, testing auth/GTFS/live API/planning workflows, checking README examples, or preparing a report of CLI workflow regressions for ptv_cli.
---

# PTV Release Validation

Use this skill to validate the released `ptv` binary, not just the local source build.

## Rules

- Test production artifacts from GitHub Releases for release validation.
- Use a temp `PTV_DATA_DIR` outside the repo so tests do not depend on local developer state.
- Never read or print secrets. Auth should resolve from env or OS keyring via normal CLI behavior.
- Keep JSON stdout parseable; warnings and notes belong on stderr.
- Treat README examples as product contracts. If an example fails, either fix behavior or fix the docs.
- Prefer human-logical inputs: route numbers/names and station names before internal API IDs.

## Release Artifact Workflow

1. Check CI/release status:
   - `gh run list --limit 10 --json databaseId,workflowName,headBranch,headSha,status,conclusion,event,createdAt,url`
   - `gh release view --json tagName,name,isDraft,isPrerelease,publishedAt,url,assets`
2. Download the current platform artifact and checksums into an approved temp path outside the repo.
3. Verify checksum before testing:
   - `shasum -a 256 <archive>`
   - compare with `checksums.txt`.
4. Extract and verify metadata:
   - `<extracted>/ptv version`
   - `<extracted>/ptv version --json`

## Baseline Commands

Run these first:

```sh
PTV_DATA_DIR=<temp-data> <ptv> auth status
PTV_DATA_DIR=<temp-data> <ptv> auth check
PTV_DATA_DIR=<temp-data> <ptv> gtfs status --no-update-check
PTV_DATA_DIR=<temp-data> <ptv> gtfs update
PTV_DATA_DIR=<temp-data> <ptv> gtfs check
```

If `gtfs status` fails because the data directory does not exist, that is a bug unless already intentionally fixed.

## Core Smoke Matrix

Use a real address supplied by the user when a location is needed. If no address is supplied, use a generic Melbourne place or station pair rather than personal addresses.

Run a mix of human and JSON workflows:

```sh
<ptv> search 'Flinders Street' --limit 5
<ptv> search 'Bourke St/Spencer St' --json --limit 3
<ptv> stops near '<address or place>' --limit 8
<ptv> stops on 'Mernda' --limit 8
<ptv> station 'Flinders Street'
<ptv> station 1071
<ptv> next 'Flinders Street' --mode train --limit 5
<ptv> next 'Melbourne University' --mode tram --route 109 --limit 3
<ptv> vehicle 243M --stop Mordialloc --route Frankston
<ptv> vehicle '17-903--1-Sun12-903738' --stop 11293 --json
<ptv> lines --mode train --limit 5
<ptv> lines 'Mernda'
<ptv> tram 109
<ptv> bus 382
<ptv> vline 1745
<ptv> disruptions --mode train --limit 5
<ptv> disruptions --json --mode tram --limit 2
<ptv> outlets 'Flinders Street' --limit 5
<ptv> fare --min-zone 1 --max-zone 2
```

Planning checks:

```sh
<ptv> plan '<origin address or place>' 'Flinders Street Station' --no-update-check
<ptv> plan '<origin address or place>' 'Box Hill Station' --arrive-by 17:00 --no-update-check
<ptv> plan 'Werribee Station' 'Belgrave Station' --depart 09:30 --no-update-check
<ptv> plan --json '<origin address or place>' 'Geelong Station' --depart 10:00 --no-update-check
```

Prefix each command with `PTV_DATA_DIR=<temp-data>` during release validation.

## What To Watch For

- `--limit` must limit rendered rows and JSON arrays where relevant.
- `station 'Flinders Street'` should prefer the train station, not a street bus stop.
- `lines 'Mernda'` should show line details, not dump all routes.
- `stops near` should accept both `lat,lng` and a geocodable address.
- Route numbers should work for human input, especially `tram 109` and `next --mode tram --route 109`.
- Route stop ordering should not lead with large blocks of `SEQ 0` entries.
- Ambiguity errors should show useful choices and not silently pick unrelated routes.
- Fare output returning all zeroes is suspicious; report it separately if still present.

## Report Format

Return a concise report with:

- release tag, commit, artifact name, checksum result
- CI/release action status
- GTFS ingest counts and freshness result
- commands that passed
- findings ordered by severity
- reproduction command for each finding
- whether the issue is behavior, docs, API/data, or test coverage

If you fix issues, rerun the affected smoke commands, then create a new tag and validate the published artifact again.
