# Waggle protocol reference (`waggle/v1`)

Plain JSON over HTTP. The live, authoritative version of this document is the
substrate's own manifest: `GET /.well-known/waggle.json`.

Base URL default: `http://127.0.0.1:7777`.

## Concepts

**Signal** — a decaying scent mark on a resource.

```json
{
  "id": "6325cd0348d0e69d",
  "agent": "scout-1",
  "resource": "repo://src/auth.go",
  "kind": "gold",
  "intensity": 4.0,
  "half_life_s": 1800,
  "note": "vuln at line 42",
  "meta": {"pr": "17"},
  "deposited_at": "2026-07-10T10:34:27Z"
}
```

Signals below `0.01` are evaporated: invisible to reads and swept from memory.
A deposit by the same `(agent, resource, kind)` **reinforces**: new intensity =
current decayed value + deposit, capped at 10, decay clock reset.

**Decay kernels.** Two kernels, selected per signal with `decay`:

- `"exp"` (default): `intensity * 2^(-age / half_life_s)` — complete
  forgetting at a constant relative rate. Right for `explored`, `heartbeat`,
  `dead-end`: knowledge that goes fully stale.
- `"power"`: `intensity * (1 + age/scale)^-alpha` with `scale` calibrated so
  the signal still halves at exactly one half-life (`alpha` defaults to 1).
  Heavy-tailed: after 10 half-lives an exponential signal is at 0.1%, a
  power-law one at ~9%. Right for `gold` and `warn`: findings that should
  fade to background, not to nothing.

Both kernels agree at age 0 and at one half-life, so `half_life_s` means the
same thing everywhere.

**Resource** — any URI. Conventions: `repo://path`, `task://id`, `topic://name`, URLs.

**Claim** — an expiring exclusive lease on a resource. Not a lock: expiry is
guaranteed, so failed agents can't wedge the swarm.

**Dance** — a broadcast on a topic (bee waggle dance), retained in a ring of
the last 1000, addressed by sequence cursor.

**Memory** — durable namespaced JSON KV. `agent/<id>` belongs to that agent;
shared namespaces are by convention.

**Channel** — a typed signal kind. A signal's `kind` string is its channel
name; registering a channel (`POST /v1/channels`) gives that kind deposit
defaults (half-life, kernel, alpha), reinforcement semantics and
cross-inhibitions. Unregistered kinds behave classically (exponential,
additive, uninhibited). Built-ins beyond the classic vocabulary:

- `bounded` — Mandelbrot robustness verdicts. Intensity is 10× the stability
  score (`escape_time / maxiter`): 0 = escapes immediately, 10 = deep bounded
  island. Half-life 7200s, power kernel with a confidence-weighted exponent
  `alpha = 2.0 − 1.5·I/10` (confident islands persist on the heaviest tail),
  **replace**-mode reinforcement (a re-measurement sets the value — two
  s=0.3 scans must not read as s=0.6), and a `low`-mode cross-inhibition of
  `gold` (ref 0.5, floor 0.25): the dead-cat-bounce filter. See
  `docs/BOUNDED_CHANNEL.md` for the math.
- `taboo` — Ọbàtálá's ethical exclusions. 24h half-life, heavy tail
  (alpha 0.5), `high`-mode inhibition of `gold` (floor 0.1) and `bounded`
  (floor 0.25). Put the justification trace in `meta` so `sniff_explain`
  can answer *why* a territory is excluded.
- `federation-health` — Vantage bridge liveness (10 min half-life).

**Evidence tier** — every signal carries `evidence_tier`, the trust ladder:
`self-report` (0.2) < `corroborated` (0.4) < `watch-derived` (0.6) <
`zangbeto-verified` (0.8) < `on-chain-anchored` (1.0). The weight applies at
read time (`effective_intensity`, `weighted` gradients); stored intensity is
never rewritten — promotion is a re-deposit at a higher tier. Unknown tiers
canonicalize to `self-report`.

**Watch** — a registered derivation rule: state transitions POSTed to a
watch's ingest endpoint become deposits at `watch-derived` trust, so systems
signal by being observed rather than by calling `mark`.

**Territory** — a URI prefix with a tempo (Ọya's heartbeat): deposits under
it that omit `half_life_s` get the territory's rhythm. Claim-velocity
evaporation stacks on top — 20 lease acquisitions under a depth-1 territory
within 10 minutes halves defaults there. Both effects apply **only inside
registered territories**: unregistered field keeps the classic deterministic
defaults, so swarms that never opt in behave identically run after run.

## Endpoints

### Agents
- `POST /v1/agents` `{id?, name?, goals?, skills?, response_thresholds?}` →
  profile. Idempotent by `id`; re-registering resumes identity and preserves
  earlier goals/skills/thresholds if omitted. `response_thresholds` maps a
  signal kind to the minimum intensity this agent responds to (division of
  labor via varied sensitivity; enforced client-side by the SDK).
- `GET /v1/agents` → `{agents: [...]}` most recently active first.
- `GET /v1/agents/{id}` → profile or 404.

### Signals
- `POST /v1/signals` `{agent, resource, kind, subtype?, intensity?, half_life_s?, decay?, alpha?, evidence_tier?, cost?, capability?, note?, meta?}`
  → the stored (possibly reinforced) signal. 400 if agent/resource/kind missing.
  `cost` is `{tokens?, wall_clock_ms?, dollars?}` — what producing the finding
  cost. It accumulates across additive reinforcement (a re-walked trail sums
  its spend) and supersedes under replace-mode. It powers cost-aware routing:
  a gold found for free and a gold found after 10k tokens are not equally
  attractive to follow.
  Unknown `decay` values canonicalize to exponential; a typed channel's
  registered kernel applies only when `decay` is omitted entirely. Unknown
  tiers canonicalize to `self-report`. Deposits on a `replace`-mode channel
  set the value instead of adding.
  **Taboo authentication.** `taboo` is the one channel that gates an *action*
  (it censors a path), not just search efficiency, so it is the one channel that
  can be authenticated. `capability` carries an Èṣù-signed token — hex(payload)
  `.` hex(ed25519-sig), payload `{agent, scope:"taboo", lineage, iat, exp}`. A
  daemon started with `-taboo-auth-key <hex-ed25519-pub>` verifies it and sets
  `taboo_authenticated` (bool) on the stored taboo signal — nil on non-taboo,
  false on an unauthenticated taboo, true on a verified one — surfaced by
  `sniff_explain` so a suppression's provenance is auditable. With
  `-taboo-auth-enforce`, a taboo deposit lacking a valid capability is refused
  (403). The core only *verifies*; issuance (and the Ọbàtálá-lineage bar for
  minting a taboo capability) lives in Èṣù (Omo-Koda2). Deterministic Ed25519
  (RFC 8032) makes the Rust issuer and Go verifier interoperate with no shared
  runtime. Other channels ignore `capability`.
- `GET /v1/sniff?resource=|prefix=|kind=|agent=|min=|min_tier=|limit=` →
  `{signals: [...]}` with intensities decayed to now, strongest first.
  Default `min` is the evaporation threshold, default `limit` 200.
  `min_tier` filters by evidence tier (`corroborated` drops self-reports).
  Each signal carries `effective_intensity` = decay × tier weight ×
  cross-inhibition.
- `POST /v1/sniff/batch` `{uris: [...(≤256)], kind?, weighted?}` →
  `{results: {uri: hotspot}}`. Each URI is a subtree prefix rollup — price N
  candidate branches for one round-trip.
- `GET /v1/gradient?prefix=|kind=|k=|depth=|weighted=|diffuse=` → `{hotspots: [...]}`
  ranked by summed live intensity: `{resource, total, resources, by_kind, agents, top_signal}`.
  Without `depth`, entries are individual resources. With `depth=N`, signals
  roll up to level N of the URI tree (`0` = scheme, `1` = first path segment,
  ...); `resources` counts distinct leaves in the group and `top_signal`
  points at the strongest real leaf signal. The gradient is self-similar:
  orient at `depth=1`, descend into the hottest subtree at `depth=2` with a
  narrowed `prefix`, repeat — O(tree depth) calls to localize the swarm's
  attention in any size field. `weighted=1` sums effective (trust-adjusted)
  intensities instead of raw. `diffuse=1` (leaf level) adds a 5% sibling
  bleed so hot neighborhoods warm unmarked resources.
- `GET /v1/explain?resource=` — the `sniff_explain` verb: every live signal
  on the resource with its tier weight, the cross-inhibitions suppressing it
  (source signal, mode, multiplier), effective intensity, raw and effective
  totals by kind, and the ambient diffusion from siblings. This is how an
  agent (or a human in the Observatory/Axiom) debugs why a hotspot reads the
  way it does.
- `GET /v1/recall?at=RFC3339&resource=|prefix=|kind=|agent=|min=|limit=` →
  `{at, signals}`: the field as it stood at a past instant, replayed from the
  journal with intensities decayed to *then*. The second read path into the
  journal — sniff answers "now", recall answers "then". No implicit
  evaporation floor: history includes the faded. Requires `-data`; 409
  without a journal.

### Channels
- `GET /v1/channels` → `{channels: [...], evidence_tiers: [...]}`.
- `POST /v1/channels` `{name, doc?, default_half_life_s?, decay_kernel?,
  default_alpha?, alpha_from_value?, alpha_min?, alpha_max?, reinforce?,
  cross_inhibits?}` — self-registration; the manifest's `channels` block
  updates immediately. Malformed inhibitions are dropped, bad floors clamp.

### Watches
- `POST /v1/watches` `{agent, name?, resource_prefix?, map?, evidence_tier?}`
  → `{watch, ingest_path}`. `map` translates outcome words to kinds
  (default `{success: gold, failure: dead-end}`); tier defaults to
  `watch-derived`.
- `POST /v1/ingest/{id}` `{resource, outcome?|kind?, subtype?, intensity?,
  half_life_s?, note?, meta?}` → the derived, deposited signal. 404 unknown
  watch, 400 unmappable event.
- `GET /v1/watches` → `{watches: [...]}` with ingest counts.

### Territories
- `POST /v1/territories` `{prefix, tempo}` — tempo scales default half-lives
  under the prefix (<1 fast, >1 slow). Longest prefix wins.
- `GET /v1/territories` → `{territories: [...]}`.

### Claims
- `POST /v1/claims` `{agent, resource, ttl_s?}` — default TTL 300s.
  - granted / renewed: `200 {granted: true, claim}`
  - held by another agent: `409 {granted: false, held_by}`
- `POST /v1/claims/release` `{agent, resource}` → `{released: bool}` (only the holder releases).
- `GET /v1/claims` → `{claims: [...]}` live leases, soonest expiry first.

### Dances
- `POST /v1/dances` `{agent, topic, payload?}` → the dance with its `seq`.
- `GET /v1/dances?since=|topic=|limit=` → `{dances: [...]}` with `seq > since`.
  Poll by keeping the max `seq` you've seen as the next cursor.

### Memory
- `PUT /v1/memory/{namespace}/{key}` (body: any JSON) → `{stored: true}`.
  Namespaces may contain `/`; the final path segment is the key.
- `GET /v1/memory/{namespace}/{key}` → `{value}` or 404.
- `GET /v1/memory/{namespace}?keys=1` → `{keys: [...]}`.
- `DELETE /v1/memory/{namespace}/{key}` → `{deleted: true}`.

### Introspection
- `GET /.well-known/waggle.json` — the manifest: all actions + conventions + live counts.
- `GET /v1/status` — health and counts.
- `GET /v1/events` — SSE stream of every mutation. Event types: `signal`,
  `claim`, `release`, `dance`, `agent`, `memory`, `memory_delete`, `channel`,
  `watch`, `territory`; each event's `data` is `{type, at, payload}`.
- `GET /` — the Observatory (human-facing live view).

## Persistence

With `-data <dir>`, every mutation appends to `<dir>/journal.jsonl`. On boot
the journal is replayed: signals keep their original timestamps (decay
continues correctly across restarts), expired claims are dropped, memory and
profiles are restored, and evaporated signals are swept.

## Extending

New capabilities must appear in four places or they don't exist: the Go
handlers, the manifest (`core/manifest.go`), the CLI (`cli/`), and the MCP
tools (`mcp/waggle_mcp.py`). Agents discover the surface from the manifest —
an undocumented endpoint is invisible to them.
