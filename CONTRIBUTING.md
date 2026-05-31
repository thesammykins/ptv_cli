# Contributing

Thanks for improving `ptv`. This repo is a Go CLI for Victorian public
transport, so every change must preserve scriptability, reliable terminal output,
and safe handling of PTV/Open Data credentials.

## Merge Requirements

A PR is mergeable only when all of these are true:

- The change has a clear user-facing purpose or fixes a documented bug.
- The PR description explains what changed, why it changed, and how it was tested.
- `go test ./...`, `go build ./...`, and `go vet ./...` pass locally or in CI.
- All touched Go files have been formatted with `gofmt`.
- New or changed behavior has regression coverage unless it is explicitly not testable.
- JSON output remains parseable: machine-readable output goes to stdout, warnings and notes go to stderr.
- No secrets, `.env` files, API keys, keyring dumps, private tokens, or credentials are committed or pasted into tests/docs.

## Required PR Contents

Each PR should include:

- Summary: 1-3 bullets describing the functional change.
- Validation: exact commands run, for example `go test ./...`, `go build ./...`, `go vet ./...`.
- User impact: commands or workflows affected, including any changed flags or JSON fields.
- Screenshots or terminal output only when it helps review a CLI formatting change.
- Follow-ups: known limitations or intentionally deferred work.

If the PR changes live API behavior, include the exact command used for dogfooding,
but do not include credential values or raw signed URLs.

## Local Checks

Run these before opening or updating a PR:

```sh
gofmt -w <files-you-touched>
go test ./...
go build ./...
go vet ./...
```

For live CLI dogfooding, build a local binary outside the repo:

```sh
go build -o /tmp/ptv .
/tmp/ptv version
```

Use `ptv auth login` for PTV Timetable API credentials and `ptv auth opendata
login` for optional Transport Victoria Open Data credentials. Do not rely on an
implicit `.env`; the CLI only reads dotenv files via explicit `--env-file`.

## Code Standards

- Keep changes minimal and idiomatic. Prefer small functions with clear control flow.
- Follow the existing Cobra command layout: one command area per file in `cmd/`, registered with `rootCmd.AddCommand` or the relevant parent command.
- Every command that returns data should honor global `--json` via `printJSON`.
- Keep human output stable and readable with `render.NewTable` where tabular output makes sense.
- Use `Australia/Melbourne` for user-facing times unless handling API UTC fields directly.
- Handle errors explicitly. Do not swallow API, file, database, or keyring errors unless the behavior is intentionally best-effort and the user still gets a useful result.
- Do not add compatibility shims, broad abstractions, or new dependencies without a concrete need.

## Tests

- Add unit tests for parsing, output shaping, API normalization, and CLI argument behavior.
- Prefer table-driven tests with named cases when covering multiple inputs.
- For API responses, test normalization using local fixtures or constructed structs, not live network calls.
- Do not write tests that depend on real credentials, keyring state, local GTFS data, wall-clock-sensitive schedules, or network availability.
- When a bug is fixed, add a regression test that would have failed before the fix.

## CLI Behavior Rules

- JSON output must stay valid JSON on stdout. Send freshness warnings, disruption warnings, and other human notes to stderr.
- Trim or normalize obvious PTV API presentation artifacts before JSON serialization when it improves machine-readability without changing meaning.
- Preserve global flags such as `--json`, `--limit`, and `--env-file` across new commands.
- Negative Melbourne coordinates need the documented `--` separator, for example:

```sh
ptv plan --arrive-by 09:00 -- "-37.8183,144.9671" "Camberwell"
```

## Data And API Gotchas

- PTV Timetable API `route_id` and GTFS static `route_id` are different namespaces.
- Match disruptions to GTFS journey legs by normalized route name/number, not raw IDs.
- GTFS `feed_mode` is the reliable mode indicator for local journey planning.
- Search terms containing `/` must be normalized before signing PTV API search paths.
- GTFS data is a rolling window. Staleness should warn users, not block planning unless the data is absent.
- GTFS Realtime is separate from the Timetable API and uses separate Open Data credentials.

## Release Notes

Tags `vX.Y.Z` trigger the release workflow. For release PRs or release-triggering
changes, call out:

- New commands or flags.
- Changed JSON fields or output shapes.
- Credential or configuration changes.
- Known live API limitations, especially when PTV returns incomplete data.
