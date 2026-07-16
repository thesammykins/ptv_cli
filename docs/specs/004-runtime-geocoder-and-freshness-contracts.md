# Spec 004 — Runtime, geocoder, and freshness contracts

**Status:** implemented and validated

**Addresses:** F11, F13, F14, F16, F17

## Objective

Make each command require only the configuration it consumes, comply with the
public Nominatim policy, and make time, cancellation, feed freshness, and local
filesystem behavior deterministic across platforms.

## Required design

### Capability-based configuration

- Split paths/static-feed/geocoder settings from PTV credentials and Open Data
  credentials. Construct a signed PTV client only in commands or optional
  overlays that need it.
- `gtfs update/status/check/realtime` catalog and local plan without live
  overlays must work without PTV credentials. A missing optional credential
  downgrades only its overlay and emits a concise stderr warning.
- Keep dotenv opt-in through `--env-file`; mise and normal startup must never
  auto-load `.env`.

### Time and lifecycle

- Load `Australia/Melbourne` explicitly for every user-facing time and include
  timezone data needed on minimal/cross-compiled systems. Tests run under at
  least `TZ=UTC` and `TZ=Australia/Perth` and produce identical Melbourne
  output.
- Create the root context with signal cancellation, use Cobra command contexts,
  and propagate it through HTTP, SQL, geocoding throttles, ingest, and routing.
- Bound JSON/protobuf reads and distinguish cancellation, timeout,
  authentication, rate limit, invalid response, and not-found errors.

### Nominatim policy

- Provide a validated runtime-switchable geocoder base URL and provider
  identity. Show OpenStreetMap/Nominatim attribution in human output and a
  structured attribution field in JSON.
- Make throttling context-aware and concurrency-safe. Public-service use must be
  coordinated across local processes at no more than one request per second;
  document that distributed release traffic requires an operator-controlled
  proxy/provider rather than pretending a client-local limiter is global.
- Add a concise disclosure before sending address-like input to the public
  service. Coordinates and known stop/station resolution should avoid geocoding.
- Key the cache by provider, endpoint, normalized query, country, viewbox, and
  response-schema version. Give entries an explicit TTL and replace the cache
  atomically with private permissions.

### Feed freshness and local paths

- Persist source URL, validators, actual byte count, publication time,
  service-date coverage, and ingest time atomically with the immutable
  generation. Store mutable last-attempt/success/backoff results separately in
  a source-keyed freshness database; offline checks remain read-only and pure.
- Base staleness first on service coverage/publication time, with ingest time
  only as a labeled fallback. Represent freshness as `current`, `changed`,
  `stale`, or `unknown`; absent validators and failed checks are never “current.”
- Back off failed automatic checks while preserving `gtfs check --force`.
- Canonicalize and validate the data directory on Unix, Windows drive, and UNC
  paths; reject filesystem roots and unexpected symlink database targets. Use
  unique private temporary files and URI-safe SQLite paths.

## Acceptance criteria

- A command/config matrix proves the minimum credentials for every command and
  verifies optional overlay degradation without malformed JSON.
- Cancellation tests interrupt an HTTP request, geocoder throttle, database
  query, and ingest without publishing partial state.
- Nominatim tests cover concurrent goroutines, two cooperating processes,
  cancellation while throttled, provider switching, attribution, privacy
  disclosure, cache expiry, corruption, and atomic replacement.
- Freshness tests cover a delayed upstream export, source URL change, missing
  validators, partial metadata failure, coverage outside the requested date,
  failed-check backoff, and a forced recheck.
- Path tests cover Unix root, Windows drive/UNC root, special characters in a
  valid directory, symlink-to-root, and an unexpected database symlink.
- Documentation identifies networked commands, the data sent to each provider,
  credential requirements, and the exact stdout/stderr contract.
