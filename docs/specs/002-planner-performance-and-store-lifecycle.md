# Spec 002 — Planner performance and store lifecycle

**Status:** implemented and validated on the disclosed comparable production snapshot

**Addresses:** F06, F07, F17

## Objective

Make `ptv plan` interactive on a production-sized feed while ensuring an update
can never replace a usable database with an empty, partial, or unindexed one.

## Required design

### Measure first

- Add separate benchmark surfaces for connection-window loading and routing so
  those phases can be compared without altering normal command output.
- Add repeatable Go benchmarks with a checked-in representative fixture and a
  documented production-snapshot harness. Record wall time, allocations, rows
  read, and peak RSS for earliest-arrival and arrive-by.

### Publish immutable database generations

- Build each update in a private sibling temporary database. Parse all
  recognized inner feeds, verify required files/headers and the calendar
  condition, require non-zero core counts, build indexes, run integrity checks
  and `PRAGMA optimize`, then close and atomically install the generation.
- Preserve the prior database on every download, parse, validation, index,
  integrity, sync, or replacement failure.
- Replace best-effort schema statements with checked, transactional
  `PRAGMA user_version` migrations. Only ignore an error that is matched to a
  named, tested compatibility condition.
- Separate read-only query opens from update/migration opens. Configure
  per-connection WAL/read-only/busy-timeout behavior through URI-safe driver
  options and propagate command contexts.
- Persist verified table counts and coverage bounds during publication rather
  than scanning 15 million stop-time rows for status.

### Preprocess for one-shot queries

- Materialize elementary connection data during ingest or in a versioned cache
  keyed by database generation and service date. Query only active services and
  the requested horizon; `LoadTimetable` must not read every trip or sort the
  whole feed per invocation.
- Use integer stop/trip-instance indexes and compact arrays in the hot scan.
  Maintain the best target label incrementally instead of scanning all targets
  for every connection.
- Persist or build once the reverse ordering needed by arrive-by. Do not clone
  and sort the entire connection set per query.
- Add a calendar-exception index led by `date`; remove redundant indexes after
  inspecting real query plans.
- Spool inner ZIPs to bounded private temporary storage rather than allocating
  up to 256 MiB per archive in memory.

## Performance budget

On the same host and dataset used by the audit (401,905 trips; 15,297,769 stop
times), a cold CLI process with a warm filesystem cache must meet both:

- median wall time over five runs no greater than 3 seconds for the audited
  one-leg earliest-arrival and arrive-by cases; and
- peak RSS no greater than 256 MiB.

The change must also improve both audited wall times by at least 10× and keep
the representative-fixture benchmark within 10% of its accepted baseline in
CI. Report cold-filesystem results separately; do not average them together.

## Acceptance criteria

- Empty ZIP, unknown inner feeds, missing required files, malformed headers,
  zero core rows, index failure, disk-full simulation, and interrupted replace
  all leave the prior generation usable.
- Schema tests prove legacy/v1 data is rejected with re-ingest guidance, v2 is
  verified without mutation, and a newer unsupported schema is rejected.
- `EXPLAIN QUERY PLAN` fixtures show date-led calendar exception lookup and
  bounded connection reads; no status command performs full `stop_times`
  counts.
- Race/concurrency tests cover planning during update and two competing update
  attempts with deterministic user-facing errors.
- The performance report includes commands, dataset generation, hardware, Go
  version, five raw samples, median, peak RSS, and focused loader/router
  benchmark results.
