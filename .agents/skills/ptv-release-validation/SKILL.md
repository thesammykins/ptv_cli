---
name: ptv-release-validation
description: Validate an already-published ptv_cli GitHub release and its production binary. Use when checking release provenance, checksums and build metadata, auth/GTFS/live API/planner smoke tests, README workflows, cross-platform assets, or release regressions.
---

# PTV release validation

Validate the requested published artifact, not a local source build. Do not tag,
push, create a release, or replace an existing artifact unless the user
separately authorizes that mutation.

## Boundaries

- Record the requested tag/commit and exact asset. If the user says “latest,”
  resolve it from GitHub rather than assuming a local tag is current.
- Download only into an approved temporary directory and verify the published
  checksum before executing the binary.
- Use a private temporary `PTV_DATA_DIR` for a clean-room ingest when full
  end-to-end validation is requested. A GTFS update is large and mutates that
  directory; do not run it for a lightweight artifact-only check.
- Never read or print secrets. Use environment/keyring resolution, or an
  explicitly authorized `--env-file` without inspecting it.
- Respect published API rate limits. Keep JSON stdout parseable and collect
  stderr separately.

## Artifact provenance

1. Inspect the release workflow and published release:

   ```sh
   gh run list --limit 10 --json databaseId,workflowName,headBranch,headSha,status,conclusion,event,createdAt,url
   gh release view '<tag>' --json tagName,targetCommitish,name,isDraft,isPrerelease,publishedAt,url,assets
   git ls-remote --tags origin 'refs/tags/<tag>' 'refs/tags/<tag>^{}'
   ```

   For an annotated tag, use the peeled `^{}` object as the release commit and
   identify the successful release-workflow run whose `headSha` matches it;
   `targetCommitish` alone is not commit provenance.

2. Select the asset matching the current OS/architecture, download it and
   `checksums.txt` to the temporary directory, and verify its SHA-256 checksum.
3. Extract it without writing into the repository.
4. Verify both metadata forms:

   ```sh
   <artifact>/ptv version
   <artifact>/ptv version --json
   ```

5. Confirm the embedded version/commit corresponds to the release tag and
   workflow commit. A successful checksum alone does not prove provenance.

## Prerequisite checks

With the artifact path represented by `<ptv>`:

```sh
PTV_DATA_DIR=<temp-data> <ptv> auth status
PTV_DATA_DIR=<temp-data> <ptv> auth check
PTV_DATA_DIR=<temp-data> <ptv> gtfs status --no-update-check
PTV_DATA_DIR=<temp-data> <ptv> gtfs update
PTV_DATA_DIR=<temp-data> <ptv> gtfs check
```

Run only the credential/network checks included in the requested scope. A new
data directory may legitimately have no database before update; the command
must return an actionable state rather than panic or corrupt JSON.

## Core smoke matrix

Use a user-supplied address when available; otherwise use generic Melbourne
places/stations.

```sh
<ptv> search 'Flinders Street' --limit 5
<ptv> search 'Bourke St/Spencer St' --json --limit 3
<ptv> stops near '<address or place>' --limit 8
<ptv> stops on 'Mernda' --limit 8
<ptv> station 'Flinders Street'
<ptv> station 1071 --mode train
<ptv> next 'Flinders Street' --mode train --limit 5
<ptv> next 'Box Hill Central' --mode tram --route 109 --limit 3
<ptv> train 'Mernda'
<ptv> tram 109
<ptv> bus 382
<ptv> vline 1745
<ptv> vehicle 243M --stop Mordialloc --route Frankston
<ptv> gtfs realtime bus-vehicle-positions --json
<ptv> lines --mode train --limit 5
<ptv> disruptions --mode train --limit 5
<ptv> disruptions --json --mode tram --limit 2
<ptv> outlets 'Flinders Street' --limit 5
<ptv> fare --min-zone 1 --max-zone 2
```

Prefix commands with `PTV_DATA_DIR=<temp-data>` for clean-room release
validation. Fetch GTFS-R only when Open Data credentials are configured.
In PowerShell, set the same isolated directory with
`$env:PTV_DATA_DIR='<temp-data>'` before invoking `& <ptv> ...`, then remove the
process-scoped value with `Remove-Item Env:PTV_DATA_DIR` after validation.

Planning matrix:

```sh
<ptv> plan '<origin>' 'Flinders Street Station' --no-update-check
<ptv> plan '<origin>' 'Box Hill Station' --arrive-by 17:00 --no-update-check
<ptv> plan 'Werribee Station' 'Belgrave Station' --depart 09:30 --no-update-check
<ptv> plan --json '<origin>' 'Geelong Station' --depart 10:00 --no-update-check
```

## Assertions

- `--limit` constrains the documented human rows and JSON arrays.
- JSON is valid on stdout; warnings remain on stderr.
- Human route/station inputs resolve predictably and ambiguities show useful
  choices.
- Station output contains the facilities promised by help text.
- Nonzero route-stop sequences and journey times are plausible; upstream zero
  stop sequences are visibly unsequenced; transfers obey explicit GTFS
  restrictions.
- Vehicle results preserve identifier namespaces and label stale observations
  honestly.
- README commands match the released binary, not unreleased source behavior.

## Report

Return the tag, commit, artifact, checksum, workflow conclusion, binary metadata,
GTFS generation/counts/freshness, credentials tested by category (never value),
commands passed, and findings ordered by severity. Give one secret-safe
reproduction command per finding and classify it as artifact/provenance,
behavior, documentation, upstream API/data, credentials, or coverage.

If defects are fixed in a separate authorized task, stop after validating the
local fix. Publishing and revalidating a replacement release require explicit
release authority.
