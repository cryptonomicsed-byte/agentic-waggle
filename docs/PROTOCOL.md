# Waggle protocol reference (`waggle/v1`)

Plain JSON over HTTP. The live, authoritative version of this document is the
substrate's own manifest: `GET /.well-known/waggle.json`.

Base URL default: `http://127.0.0.1:7777`.

**Port collision note:** `:7777` is also Omo-Koda2's kernel default. Run waggled
with `--addr :7778` when co-located with `omokoda-core`.

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

Current intensity at time *t* is `intensity * 2^(-(t - deposited_at) / half_life_s)`.
Signals below `0.01` are evaporated: invisible to reads and swept from memory.
A deposit by the same `(agent, resource, kind)` **reinforces**: new intensity =
current decayed value + deposit, capped at 10, decay clock reset.

**Resource** — any URI. Conventions: `repo://path`, `task://id`, `topic://name`, URLs.

**Claim** — an expiring exclusive lease on a resource. Not a lock: expiry is
guaranteed, so failed agents can't wedge the swarm.

**Dance** — a broadcast on a topic (bee waggle dance), retained in a ring of
the last 1000, addressed by sequence cursor.

**Memory** — durable namespaced JSON KV. `agent/<id>` belongs to that agent;
shared namespaces are by convention.

## Endpoints

### Authentication

Write verbs (`POST /v1/signals`, `/v1/claims`, `/v1/claims/release`,
`/v1/dances`, `/v1/watches`, `/v1/ingest/{id}`, `PUT/DELETE /v1/memory/…`)
require an Èṣù session token obtained at registration:

```
POST /v1/agents  →  response includes "token": "{agent_id}:{bearer}"
```

Pass it on writes:

```
X-Waggle-Token: {agent_id}:{bearer}
# or
Authorization: Bearer {agent_id}:{bearer}
```

**Enforcement modes:**

| Mode | Behaviour |
|------|-----------|
| default (open) | Missing/invalid token logged, request allowed. Backward-compatible. |
| `--require-auth` | Missing/invalid token → `401`. Use in production. |

**Taboo signals** additionally require an ed25519 capability in the body:

```json
{"kind": "taboo", "capability": "<hex_payload>.<hex_sig>", ...}
```

Active only when `--taboo-auth-key <hex_pubkey>` is set. Capability minted by
`TabooCapIssuer` in `Omo-Koda2/omokoda-core/src/waggle/taboo_cap.rs`.

**Mark throttle:** 30-burst / 0.5 per second per agent. Excess → `429`.

**Read verbs** (`GET /v1/sniff`, `/v1/gradient`, `/v1/claims`, `/v1/dances`,
`GET /v1/memory/…`, `/v1/events`) are always open — scent is public by design.

### Sybil detection

`GET /v1/rings` — agents that registered from the same `X-Waggle-Origin` within
10 minutes, cluster size ≥ 3. Returns `{rings: [{origin, members}]}`. Also
visible in `GET /v1/status` as `suspected_rings` count.

### Agents
- `POST /v1/agents` `{id?, name?, goals?, skills?}` → flat profile + `"token"`.
  Idempotent by `id`; re-registering resumes identity and rotates the session token.
- `GET /v1/agents` → `{agents: [...]}` most recently active first.
- `GET /v1/agents/{id}` → profile or 404.

### Signals
- `POST /v1/signals` `{agent, resource, kind, intensity?, half_life_s?, note?, meta?}`
  → the stored (possibly reinforced) signal. 400 if agent/resource/kind missing.
- `GET /v1/sniff?resource=|prefix=|kind=|agent=|min=|limit=` → `{signals: [...]}`
  with intensities decayed to now, strongest first. Default `min` is the
  evaporation threshold, default `limit` 200.
- `GET /v1/gradient?prefix=|kind=|k=` → `{hotspots: [...]}` resources ranked by
  summed live intensity: `{resource, total, by_kind, agents, top_signal}`.

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

### Watches (Ògún tool routing)

The Rust waggle client (`omokoda-core/src/waggle/mod.rs`) registers a watch
once at startup and posts tool outcomes to it. The server converts outcomes to
field signals automatically.

- `POST /v1/watches` `{agent, name?, resource_prefix?}` → `{watch: {id, ...}}`.
  `resource_prefix` filters which resources this watch ingests (e.g. `tool://`).
- `GET /v1/watches` → `{watches: [...]}`.
- `POST /v1/ingest/{watch_id}` `{resource, outcome, note?}` → `{signal, watch_id}`.
  `outcome` must be `"success"` (→ `gold` signal) or `"failure"` (→ `dead-end`).
  Intensity 4.0, half-life 3600s.

### Introspection
- `GET /.well-known/waggle.json` — the manifest: all actions + conventions + live counts.
- `GET /v1/status` — health and counts including `watches`, `suspected_rings`.
- `GET /v1/rings` — Sybil ring report (see Authentication above).
- `GET /v1/events` — SSE stream of every mutation. Event types: `signal`,
  `claim`, `release`, `dance`, `agent`, `memory`, `memory_delete`, `watch`;
  each event's `data` is `{type, at, payload}`.
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
