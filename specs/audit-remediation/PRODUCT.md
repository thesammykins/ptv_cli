# Audit remediation — product specification

**Feature:** `audit-remediation`

**Status:** implemented and validated

**Last updated:** 16 July 2026

This feature consolidates the approved outcomes in the
[code-quality audit](../../docs/audits/2026-07-16-code-quality-audit.md) and
[remediation specs 001–004](../../docs/specs/) into one user-facing contract.
No issue tracker ticket is in use, so the short feature name is used for this
spec directory.

## Problem

`ptv` can currently return plausible-looking results that do not always obey
the source transport data. It can allow prohibited transfers, ignore station
pathways or boarding restrictions, merge service instances across dates, and
mis-handle times around Melbourne daylight-saving transitions. Its primary
planning command can also take close to a minute on a production-sized feed.

Live commands have related trust problems: some official station data is
dropped, identifiers from unrelated PTV and GTFS Realtime namespaces are
treated as interchangeable, and stale or undated vehicle observations may be
presented as current. Local and public-data workflows unnecessarily require PTV
credentials, while JSON, freshness, cancellation, and geocoding behavior are
not sufficiently predictable for scripts or users.

These issues undermine the CLI's central promise: a fast, trustworthy terminal
view of Victorian public transport that remains useful with local static data
and becomes richer, but not less reliable, when live services are available.

## Product outcome

After remediation, users can rely on `ptv` to:

- return only journeys permitted by the imported GTFS data;
- plan interactively against a production-sized local feed;
- keep the last usable dataset when an update fails;
- distinguish current, stale, unknown, and unavailable live information;
- expose official station and vehicle information without confusing identifier
  namespaces;
- run local and public-data commands with only the credentials they consume;
- produce stable, parseable JSON while keeping warnings on stderr; and
- behave consistently across machines, time zones, cancellation, and supported
  filesystem layouts.

## Users and primary journeys

### A traveller planning locally

The traveller can update the static feed and plan a journey without PTV
Timetable credentials. Results account for real access, transfer, pathway,
boarding, alighting, service-day, and walking constraints. If optional live
overlays are unavailable, the static journey still succeeds and the limitation
is explained without corrupting output.

### A traveller checking current service

Station details include the location and facilities supplied by PTV. Vehicle
observations identify what was actually observed, when it was observed, and
whether it is current. An outage, authentication failure, or stale observation
is never described as “not found” or “current.”

### A script or agent consuming JSON

Every command documents and emits a normalized command-specific shape. JSON is
the sole stdout payload, warnings remain on stderr, times and identifiers carry
unambiguous meaning, and internal upstream identifiers are not leaked as public
identity.

### A user maintaining local data

A failed, interrupted, empty, malformed, or incomplete update leaves the
previous dataset intact and usable. Status explains the dataset's source,
coverage, and confidence rather than inferring freshness from installation time
alone.

## Required behavior

### 1. Trustworthy journey planning

- Explicit GTFS rules always take precedence over inferred walking or vehicle
  continuation. A forbidden transfer is never made available through a
  synthesized shortcut.
- Station hierarchy, directed pathways, transfer duration and direction,
  same-stop change time, boarding policy, and alighting policy affect journey
  feasibility.
- Pickup/drop-off values requiring advance arrangement or driver coordination
  are excluded from automatic journeys by default. A user may opt in explicitly
  and receives a visible conditional-service warning.
- Multi-step walking paths are discoverable when every step is permitted. The
  planner does not require the feed to provide a pre-closed walking graph.
- A trip occurrence belongs to one feed and one service date. Reused trip or
  block strings cannot connect unrelated feeds, dates, or vehicle movements.
- A required in-seat continuation is presented as distinct transit legs joined
  by an explicit “stay onboard” instruction and does not increment transfers.
- Melbourne service-day semantics are correct through midnight, times beyond
  `24:00:00`, and both daylight-saving transitions.
- Planning considers at least the 36-hour search horizon around the requested
  direction, including the service days needed to cover it.
- Coordinate and place endpoints include their actual access and egress walking
  time. Nearby stops are not treated as simultaneous origins or destinations.
- Planning from a location to itself returns an explicit zero-leg journey at
  the requested time, not an empty result or a zero-value timestamp.
- When the requested date falls outside known feed coverage, the user receives
  a specific coverage explanation rather than an unexplained “no journey.”

### 2. Interactive performance

- On the audited production-sized dataset, both earliest-arrival and arrive-by
  planning meet the agreed three-second median wall-time and 256 MiB peak-memory
  budgets for a cold CLI process with a warm filesystem cache.
- Both audited cases improve by at least 10× compared with the recorded
  68.57-second and 48.87-second baselines.
- Optional diagnostics may explain planning phases, but never alter normal
  stdout or expose credentials.

### 3. Safe static-data lifecycle

- A feed update becomes visible only after the complete candidate dataset has
  passed structural, non-empty, indexing, and integrity checks.
- Any failure before publication leaves the previous generation available to
  readers.
- Planning can continue safely while another process updates the feed.
- Two competing updates produce deterministic, actionable feedback and cannot
  corrupt or partially publish data.
- Status reports use persisted, verified counts and coverage. A normal status
  check does not require an expensive full scan of the feed.

### 4. Exact and honest live-data behavior

- Station output reflects the current official PTV Stop Details contract,
  including its nested location and available facility, accessibility,
  staffing, and disruption information.
- PTV run references, static GTFS trip identifiers, GTFS Realtime entity
  identifiers, public vehicle labels, and internal vehicle identifiers remain
  distinct in matching, display, and JSON.
- Cross-source matching occurs only when there is enough service date, time,
  route, and trip context to prove the relationship.
- A vehicle observation is called current only when it has a valid observation
  time no more than 90 seconds old. Other observations are labeled stale,
  last-seen, or unknown and include the observation time and age when known.
- Public vehicle labels are used as user-facing identity. Upstream-declared
  internal vehicle IDs are excluded from default human and JSON output.
- Each live request uses the documented authentication contract and respects
  published service limits. The current GTFS Realtime contract sends exactly
  one `KeyID` header; it does not guess alternate header spellings or send the
  compatibility API-ID value without newer authoritative per-feed evidence.
- Network, authentication, rate-limit, timeout, invalid-response, and not-found
  outcomes remain distinguishable to the user.

### 5. Capability-based configuration

- A command asks only for credentials needed by its required capabilities.
- Static GTFS catalog/update/status/check operations and local planning work
  without PTV Timetable credentials.
- A missing optional live credential disables only that overlay, emits a concise
  stderr warning, and does not invalidate otherwise successful JSON.
- Dotenv remains explicitly opt-in through `--env-file`; normal startup and
  project tooling never load `.env` automatically.

### 6. Stable command and JSON contracts

- `--json` means normalized command output, not a dump of an upstream transport
  object.
- Existing top-level shapes are preserved through the current major release
  where possible. Any necessary removal or rename has a documented deprecation
  and next-major path.
- Every command documents time format, optional fields, enum meanings,
  identifier namespaces, attribution, and stdout/stderr behavior.
- All user-facing times are rendered in `Australia/Melbourne`, regardless of
  the host machine's local time zone.
- Cancellation reaches active HTTP, geocoding, database, ingest, and routing
  work promptly and does not publish partial state.
- Responses and local temporary artifacts are bounded so malformed or hostile
  input cannot consume unlimited memory or disk.

### 7. Geocoding, freshness, and local-state transparency

- Known stops, stations, and coordinate input avoid geocoding. Before an
  address-like query is sent to a public geocoder, the CLI gives a concise
  disclosure.
- Human and JSON results contain the required OpenStreetMap/Nominatim
  attribution when that provider contributes data.
- The geocoding provider is switchable at runtime, caching is provider-aware
  and expires predictably, and public Nominatim use is coordinated across local
  processes at no more than one request per second.
- The CLI states that distributed release traffic requires an
  operator-controlled provider or proxy; it does not claim that a local limiter
  governs all installations.
- Feed status distinguishes `current`, `changed`, `stale`, and `unknown` using
  source, publication, coverage, and check evidence. Missing validators or a
  failed check never imply current data.
- Automatic failed checks back off, while an explicit forced check remains
  available.
- Invalid roots, unsafe database symlinks, and ambiguous data paths are rejected
  consistently on supported Unix and Windows path forms.

## Invariants and edge cases

The following hold across all commands and output modes:

1. Explicit source prohibitions win over inferred convenience.
2. Absence of evidence is not freshness, identity, or availability evidence.
3. Identical strings in different identifier namespaces are not equal by
   accident.
4. A partial candidate dataset never replaces a usable dataset.
5. Optional network enrichment never blocks a valid local result.
6. JSON stdout remains one valid document even when warnings or degradation
   occur.
7. Host time zone does not change Victorian transport times.
8. Cancellation cannot publish state that did not pass normal validation.
9. A failed API request is not silently converted to a successful empty result.
10. Sensitive credentials, signed URLs, internal vehicle IDs, and `.env`
    contents are never printed by diagnostics or normal output.

Required edge-case coverage includes:

- GTFS transfer types 0–5, conflicting specificity, forbidden same-stop
  transfer, parent stations, one-way pathways, and physical barriers;
- pickup/drop-off restrictions while boarding, alighting, and remaining on the
  same vehicle, including explicit opt-in for conditional values 2 and 3;
- multi-hop walking, cross-feed block collisions, consecutive dates reusing a
  raw trip ID, midnight overflow, and Melbourne DST start/end;
- same origin/destination, coordinate access/egress, and query dates outside
  feed coverage;
- missing or stale realtime timestamps, future clock skew, identifier
  collisions, and upstream outages;
- empty or malformed feeds, disk exhaustion, interruption, publication failure,
  locked databases, and concurrent updates;
- missing optional credentials, process cancellation, public-geocoder
  throttling across processes, cache corruption/expiry, and provider changes;
  and
- Unix roots, Windows drive and UNC roots, special characters, and unsafe
  symlinks.

## Success criteria

The remediation is complete only when all of the following are true:

- The journey-correctness fixtures demonstrate every required GTFS and
  service-day behavior, and no returned journey violates a boarding, alighting,
  transfer, pathway, or service-instance rule.
- Earliest-arrival and arrive-by produce equivalent feasible results on a
  reversible reference network.
- The production-snapshot performance report meets the time, memory, and 10×
  improvement budgets with five disclosed raw samples per case.
- Every tested update failure and concurrent-reader scenario preserves the last
  usable dataset.
- Official PTV and GTFS Realtime fixtures verify modeled fields, request count,
  authentication, identity separation, and freshness classifications.
- The command/configuration matrix proves the minimum credential requirement
  and graceful optional-overlay degradation for every command family.
- Golden JSON tests prove documented shapes, Melbourne timestamps, public
  identifier semantics, attribution, warning separation, and absence of
  internal vehicle IDs.
- Cancellation, geocoder policy, freshness, unsafe-path, and bounded-resource
  tests pass on all supported platforms represented in CI.
- Existing build, vet, unit, race, and formatting gates remain green.

## Validation approach

Validation must map directly to the behavior above:

- Use small table-driven and property fixtures for journey legality and time
  boundaries.
- Use captured official response shapes through local test servers for external
  API behavior; live endpoints are a final compatibility probe, not the only
  regression coverage.
- Use fault-injection and concurrency tests for database publication and
  cancellation.
- Use golden documents for JSON and human-output distinctions that affect user
  trust.
- Run the documented production-snapshot benchmark separately from CI's small,
  repeatable regression benchmark and publish the raw results.
- Exercise time rendering under host zones other than Melbourne and exercise
  path behavior on supported operating-system forms.

## Non-goals

- Realtime delays are not incorporated into static journey routing by this
  remediation.
- Fare optimization and a broader journey-ranking redesign are not included.
- No new GUI, web interface, or interactive visual design is introduced.
- A breaking JSON redesign is not performed within the current major release.
- The CLI does not attempt to globally coordinate Nominatim traffic across all
  installed copies; operators serving distributed traffic must provide a
  suitable proxy or provider.
- `.env` is not auto-loaded, migrated, or modified.

## Design note

This is terminal and data-contract work with no visual UI surface. No Figma mock
exists or is required. Human-readable terminal wording and normalized JSON are
validated as output contracts rather than visual designs.
