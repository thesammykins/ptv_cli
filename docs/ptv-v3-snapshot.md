# Embedded PTV v3 enrichment snapshot

The CLI ships a reviewed, normalized subset of PTV Timetable API v3 data so
some v3-only enrichment remains available without distributing or requesting
v3 credentials at runtime.

The snapshot is supplementary. Local GTFS remains authoritative for static
topology, schedules, route identities, and journey planning. GTFS-Realtime
remains authoritative for live service state. The snapshot contains only myki
outlet records and explicitly seeded station-facility fields. It deliberately
does not contain routes, route types, directions, departures, runs,
disruptions, vehicle positions, or any other data that could be used to route
a journey after becoming stale.

The embedded files are:

```text
internal/v3static/data/snapshot.json
data/ptv-v3/station-seeds.txt
```

It contains a schema version, generation time, source identifier, attribution,
and a SHA-256 hash of the normalized records. The generator sorts records and
preserves the previous generation time when the content hash is unchanged, so
an update run produces no diff when the upstream data has not changed.

## Local generation

Use an explicitly authorized dotenv file or the normal environment/keyring
resolution. The command never prints credentials or signed URLs:

```sh
go run ./tools/ptv-v3-snapshot --env-file .env \
  --output internal/v3static/data/snapshot.json
```

Station-facility enrichments are opt-in and bounded:

```sh
go run ./tools/ptv-v3-snapshot --env-file .env \
  --station 0:1071 \
  --output internal/v3static/data/snapshot.json
```

`--station` values and `data/ptv-v3/station-seeds.txt` entries use
`route_type:ptv_stop_id`. The generator does not call the routes or directions
endpoints, so a refresh cannot add static route data accidentally. The
`--migrate-legacy` option is a one-time local migration for older snapshots;
it removes legacy route records without credentials.

## Automated refresh

`.github/workflows/update-v3-snapshot.yml` runs monthly and supports manual
dispatch. It reads `PTV_API_KEY` and `PTV_API_USERID` from GitHub Actions
Secrets, generates the normalized file, validates it, and opens a pull request
only when the content hash changes. It does not publish directly.

The PTV Timetable API data is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
The exact requested attribution, source URL, modification statement,
responsibility disclaimer, and non-endorsement statement are shipped in
`NOTICE` and are included as `source_notice` in JSON results that use bundled
snapshot data. The API is dynamic; the snapshot is not a substitute for live
route, timetable, disruption, or vehicle data.
