# `fractal-oracle` — standalone service interface (spec)

Status: **design spec, not yet implemented.** This is the interface for
promoting the Fractal Oracle from a Wasm species embedded in Axiom
(`Axiom/agents/oracle/src/lib.rs`) to a standalone service the whole
ecosystem queries — the one piece that unblocks ỌṢỌVM's build-time gate,
LOOM's live verdicts, Ṣàngó's anchoring, and Vantage's cross-ecosystem
comparison without four divergent fractal implementations
(Connection Map v2, `docs/CONNECTION_MAP_V2.md` in the Omo-Koda2 repo, §8.2).

## 1. Shape of the service

Plain JSON over HTTP, exactly like `waggled` — same reasons: zero-dependency
clients must be able to call it from any sandbox with nothing but a socket.

- **Language:** Rust, `std` only (the ecosystem's Rust rule), or Julia
  stdlib. The reference numerics already exist in `no_std` Rust in the
  embedded oracle; the service wraps that same core behind HTTP, so Rust is
  the path of least divergence.
- **Bind:** `127.0.0.1:7778` by default (`waggled` owns `:7777`), `-addr`
  flag to override.
- **Discovery:** `GET /.well-known/oracle.json` — MCP-style manifest listing
  the tools, their params, and the service's determinism contract (§6). Same
  philosophy as Waggle: the manifest is the live authoritative interface;
  an unlisted tool doesn't exist.
- **Axiom keeps its embedded Wasm copy** for offline/demo use. §6's
  determinism contract plus a shared fixture suite is what keeps the two
  from drifting: identical inputs must produce byte-identical `esc` arrays
  and equal scores.

## 2. Invocation

One generic entry point, MCP-shaped:

```
POST /v1/invoke
{"tool": "escape_time_risk", "arg": {"re": -0.75, "im": 0.1, "depth": 2}}
→ {"tool": "...", "result": {...}, "oracle": {"version": "...", "scans": 12345}}
```

plus per-tool `GET` conveniences (`GET /v1/escape_time_risk?re=&im=&depth=`)
for humans and `curl`. Arguments are named JSON fields, not the embedded
oracle's positional comma-list — the Wasm ABI kept args as a flat byte
string out of necessity; a service has no such constraint and named args
are what the MCP tool wrappers want anyway.

## 3. Tools

The five tools are the embedded oracle's, with the same result fields, so
existing Axiom-side consumers port by changing transport only.

### `mandelbrot_scan`
Escape-time grid over a region.
`{re0, re1, im0, im1, width ≤ 220, height ≤ 160, depth?}` →
`{w, h, maxiter, esc: [...]}` row-major, `esc[i] = escape_time`, `maxiter`
meaning bounded.

### `escape_time_risk`
Single-point fragility.
`{re, im, depth?}` → `{c: [re, im], escape, maxiter, bounded, stability,
risk, verdict}` with `stability = escape/maxiter`, `risk = 1 − stability`,
`verdict ∈ {"robust island", "fragile boundary", "escape zone"}` (bounded /
`stability > 0.5` / otherwise — unchanged from the embedded oracle).

### `robust_island_query`
`{re, im, depth?}` → `{bounded, depth: escape, stability, island}`.

### `fractal_signal_filter`
Series classification: accumulation vs breakout.
`{points: [[re, im], ...]}` → `{points, bounded, bounded_fraction, signal}`.

### `swarm_stability_map`
Aggregate stability of a swarm mapped into parameter space.
`{points: [[re, im], ...]}` → `{agents, bounded, stability, verdict}`.
This output is the designated input to Yemọja's spawn throttling
(Connection Map §8.3): `stability` trending down across successive calls on
the live swarm's parameter embedding means *stop adding agents to this
territory*, independent of any individual strategy's verdict.

## 4. Field write-back

Any tool call may carry a `deposit` block:

```json
{"tool": "escape_time_risk",
 "arg": {"re": -0.75, "im": 0.1},
 "deposit": {"waggle": "http://127.0.0.1:7777", "agent": "fractal-oracle",
             "resource": "loom://strategy/sniper/params"}}
```

On success the service itself deposits the verdict on the `bounded`
channel — intensity `10·stability`, `α(s)` and replace-mode reinforcement
per [`BOUNDED_CHANNEL.md`](BOUNDED_CHANNEL.md), `meta` carrying
`{escape, maxiter, verdict, probe: [re, im]}` — at `evidence_tier:
watch-derived`, since the number came from the measuring instrument rather
than the interested party. A LOOM agent that instead computes locally and
self-reports deposits at `self-report` and needs corroboration to be
weighted equally: routing verdicts through the shared service is what buys
the trust promotion, which is the incentive that keeps everyone on one
implementation.

Without a `deposit` block the service is a pure function and touches
nothing.

## 5. The shared depth convention (Connection Map §8.4)

Waggle's `gradient?depth=N` (URI-tree rollup) and the oracle's iteration
budget are the same gesture — recursive refinement toward more detail — so
they share one parameter convention. Everywhere in this service:

```
depth N  →  maxiter = 100 · 2^N,  clamped to [8, 2000]
            (N: 0 → 100, 1 → 200, 2 → 400, 3 → 800, 4 → 1600, 5+ → 2000)
```

Raw `maxiter` remains accepted for callers that need exact control, but
`depth` is the documented interface, and the number an agent picks answers
the same question in both APIs: *how hard should I look before I trust the
answer?* Cheap first pass at `depth=0`, commit real capital only on a
`depth≥3` verdict — the same orient-then-descend loop agents already run
against the scent gradient. The clamp ceiling (2000) matches the embedded
oracle's, so no depth expressible here can diverge from what the Wasm copy
can verify.

`escape_time_risk` results always echo the resolved `maxiter`, and the
`bounded` deposit's `meta` carries it, because a `stability` score is only
comparable at equal iteration budget — consumers comparing verdicts across
time (regime-shift detection) or across ecosystems (Vantage §4.5) must
compare like depth with like.

## 6. Determinism contract

The embedded oracle's core property — anyone can re-verify any result —
survives the move to a service, and the manifest states it:

- IEEE-754 `f64` throughout; iteration `z ← z² + c`; escape test
  `zr² + zi² > 4.0` checked *before* the update step; row-major grids;
  grid coordinates by linear interpolation over `(w−1, h−1)` divisions.
  This is the exact loop in `lib.rs::escape_time` — the service vendors
  that function, not a reimplementation.
- No randomness, no time dependence, no state that affects results
  (`scans`/`islands` counters are telemetry, echoed but never read).
- Fixture suite shared between this repo, the service, and Axiom's embedded
  copy: a set of `(tool, arg) → result` golden files that both
  implementations must match byte-for-byte on the numeric payload. A
  Zangbeto verification of an oracle receipt is precisely a replay against
  this contract, which is what lets an oracle-originated `bounded` signal
  be promoted to `zangbeto-verified` (Connection Map §1.9) and, past the
  significance threshold, anchored by Ṣàngó (§7.3) — a robustness claim is
  only worth anchoring on-chain because any party can recompute it.

## 7. Operational endpoints

- `GET /v1/health` → `{ok, version, scans, islands, uptime_s}` — feeds
  Vantage's `federation-health` meta-signal when the oracle is consumed
  across a bridge.
- `GET /.well-known/oracle.json` → manifest (tools, params, defaults, depth
  table, determinism contract, fixture-suite hash).

## 8. Build order

1. Extract `escape_time` + tool bodies from `Axiom/agents/oracle/src/lib.rs`
   into a shared crate; the Wasm species and the service both depend on it.
2. Service binary: HTTP listener, `/v1/invoke`, manifest, health — `std`
   only, mirroring `cli/wag`'s zero-crate discipline.
3. Golden fixture suite; wire into both builds' tests.
4. `deposit` write-back (depends on the `bounded` channel landing in
   `waggled` first — Connection Map dependency order puts typed channels at
   step 1, this service at step 3).
5. MCP tool wrappers in `mcp/` so sandboxed agents get the tools without
   speaking HTTP themselves.
