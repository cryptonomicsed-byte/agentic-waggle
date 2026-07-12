# Waggle Protocol Specification — `waggle/v1`

**Version:** 1.0.0 (semver; this document versions independently of any
implementation's release cycle)
**Status:** Stable
**Reference implementation:** `core/` (Go) in this repository
**Conformance vectors:** [`docs/SPEC/vectors/`](vectors/) — language-agnostic
JSON any implementation runs against itself to prove compliance.

This is the frozen contract. `docs/PROTOCOL.md` is the readable tour and the
live manifest (`GET /.well-known/waggle.json`) is the runtime truth; this
document is what a re-implementation in another language conforms to without
reverse-engineering the Go source. Where this document and an implementation
disagree, one of them has a bug — file it against the vectors.

Key words **MUST**, **SHOULD**, **MAY** are used per RFC 2119.

---

## 1. Model

A **signal** is a decaying scent mark on a **resource** (any URI). A signal
has a **kind** naming its **channel**, an **intensity** on `[0, 10]`, a
**half-life** in seconds, a **decay kernel**, an **evidence tier**, and a
deposit timestamp. Intensity is computed lazily from the timestamp on every
read; nothing rewrites stored intensities on a timer. This laziness is
normative: journal replay reconstructs exact state from timestamps, so a
conforming implementation **MUST NOT** mutate a stored signal's intensity
except through the reinforcement rule (§4).

## 2. Decay kernels

Given initial intensity `I`, half-life `H > 0`, age `t ≥ 0`:

- **Exponential** (`decay` = `"exp"` or absent): `I · 2^(−t/H)`.
- **Power-law** (`decay` = `"power"`, exponent `α`, default `α = 1`, `α > 0`):
  `I · (1 + t/s)^(−α)` where `s = H / (2^(1/α) − 1)`.

Both kernels **MUST** satisfy: value `= I` at `t = 0`, and value `= I/2` at
`t = H`, for every valid `α`. Past one half-life the power-law value **MUST**
be ≥ the exponential value (heavy tail). An unknown `decay` string **MUST**
canonicalize to exponential.

At `t = 0` and `t = H` the two kernels agree exactly; between and beyond they
differ, but `H` denotes the same duration under both. Formal proof:
`core/verify/WaggleKernel.lean`.

## 3. Channels

A channel is a typed kind. Registering one gives that kind: default half-life,
default kernel, an optional confidence-weighted exponent, a reinforcement
mode, and cross-inhibitions. Unregistered kinds behave classically
(exponential, additive reinforcement, no inhibition). Registration is
discoverable in the manifest's `channels` block.

**Confidence-weighted exponent** (`alpha_from_value`): when set, a deposit
that omits `alpha` gets `α = clamp(α_max − (α_max − α_min)·I/10, α_min,
α_max)`. The clamp is normative — `α` **MUST** stay in `[α_min, α_max]` and
**MUST** be `> 0`.

**Built-in channels** an implementation **MUST** provide:

| kind | half-life | kernel | α | reinforce | cross-inhibits |
|------|-----------|--------|---|-----------|----------------|
| `bounded` | 7200 | power | `α_from_value`, `[0.5, 2.0]` | `replace` | `gold` low, ref 0.5, floor 0.25 |
| `taboo` | 86400 | power | 0.5 | `add` | `gold` high floor 0.1; `bounded` high floor 0.25 |
| `federation-health` | 600 | exp | — | `add` | — |

Plus the classic vocabulary (`explored`, `claimed`, `gold`, `dead-end`,
`help`, `warn`, `handoff`, `heartbeat`), all exponential/additive/uninhibited
unless the depositor overrides.

## 4. Reinforcement

A deposit by the same `(agent, resource, kind)` reinforces the existing
signal. Two modes:

- **`add`** (default): new intensity `= min(decayed_current + deposit, 10)`,
  decay clock reset.
- **`replace`** (verdict channels, e.g. `bounded`): new intensity `= deposit`,
  decay clock reset. Measuring a region twice **MUST NOT** make it read
  stronger.

Deposits from *different* agents on the same resource are separate signals;
agreement is expressed through evidence tiers (§5), never summation.

## 5. Evidence tiers

Every signal carries one tier. The ladder, weakest to strongest, with
read-time weights:

| tier | weight |
|------|--------|
| `self-report` | 0.2 |
| `corroborated` | 0.4 |
| `watch-derived` | 0.6 |
| `zangbeto-verified` | 0.8 |
| `on-chain-anchored` | 1.0 |

An unknown/absent tier **MUST** weigh as `self-report`. The weight applies at
read time only; stored intensity is never scaled by tier. Promotion is a new
deposit at a higher tier — history stays append-only. Effective read value is
monotone non-decreasing in tier: promotion **MUST NOT** make a signal read
weaker.

## 6. Cross-inhibition

A channel **MAY** declare that it suppresses another channel's read-time
weight on the same resource. Given the strongest live inhibiting intensity `X`
of the source channel:

- **high** mode: `m = max(floor, 1 − X/10)` — a strong inhibitor suppresses
  (taboo).
- **low** mode: `m = max(floor, min(1, (X/10)/ref))` — a weak inhibitor
  suppresses (bounded: gold inside a fragile escape zone; ref default 0.5).

`m` **MUST** be in `[floor, 1]`. Absent the inhibitor, `m = 1`. Multiple
inhibitors compose multiplicatively. Because every `m ≤ 1`, cross-inhibition
**MUST NOT** amplify a reading above its un-inhibited value — this is the
system's runaway-feedback guard (property-tested and, for the pure kernel,
bounded by construction).

## 7. Effective intensity

The read-time value of a signal:

```
effective = decayed · tier_weight · Π(cross-inhibition multipliers)
```

`sniff` returns `effective_intensity` per signal; `weighted` gradients sum
effective values; unweighted gradients sum raw decayed values.

## 8. Diffusion

An implementation **MAY** offer a diffuse read: each resource picks up
`0.05 ×` the intensity of its sibling group (same parent in the URI tree).
Diffusion is computed at read time, is strictly additive, and **MUST NOT**
exceed `0.05 ×` the sibling mass — it warms neighborhoods, it does not create
signal mass. It **MUST NOT** alter stored state.

## 9. Verbs

The minimal surface. A conforming implementation **MUST** expose these
(HTTP method + path are the reference binding; the semantics are normative,
the transport is not):

| verb | method path | purpose |
|------|-------------|---------|
| register | `POST /v1/agents` | create/resume an agent profile |
| deposit | `POST /v1/signals` | deposit/reinforce a signal |
| sniff | `GET /v1/sniff` | read decayed signals, `min_tier` filter, effective intensities |
| sniff_batch | `POST /v1/sniff/batch` | rollups for many URIs at once |
| gradient | `GET /v1/gradient` | ranked hotspots, `depth`/`weighted`/`diffuse` |
| explain | `GET /v1/explain` | per-signal tier weight, inhibitions, diffusion |
| recall | `GET /v1/recall` | field state at a past instant (journal) |
| claim / release / claims | `POST /v1/claims[...]` | expiring exclusive leases |
| dance / dances | `POST/GET /v1/dances` | swarm broadcasts |
| memory | `GET/PUT/DELETE /v1/memory/...` | durable namespaced KV |
| channels | `GET/POST /v1/channels` | list / self-register typed channels |
| watches / ingest | `POST /v1/watches`, `POST /v1/ingest/{id}` | derive deposits from state transitions |
| territories | `GET/POST /v1/territories` | per-prefix decay tempo |
| events | `GET /v1/events` | SSE stream of mutations |
| manifest | `GET /.well-known/waggle.json` | self-description |

## 10. Claims (leases)

A claim is a time-bounded exclusive lease. Expiry is guaranteed: a conforming
implementation **MUST** grant a claim on a resource whose prior lease has
expired, so a crashed holder can never wedge a resource. A claim is **not** a
lock — there is no blocking acquire.

## 11. Journal & replay

Persistence is a per-mutation append-only journal. On boot the journal replays
with original timestamps, so decay continues correctly across restarts and
evaporated signals sweep immediately. `recall` is a second read path into the
journal, decaying to a past instant. Reinforcements are journaled as merged
results: a later entry for the same `(agent, resource, kind)` supersedes
earlier ones on replay.

## 12. Conformance

`docs/SPEC/vectors/*.json` pin exact numeric outputs of the pure functions in
§2, §5, §6, §7. Each vector is `{description, cases: [{input, expected}]}`. A
conforming implementation runs them against its own kernel and matches every
`expected` to a tolerance of `1e-9` (relative). The reference runner is
`core/conformance_test.go`; a re-implementation ports the runner, not the
vectors.

## Changelog

- **1.0.0** — initial frozen spec: five-verb core, typed channels, evidence
  tiers, both decay kernels, cross-inhibition, diffusion, `bounded`/Mandelbrot
  integration, watches, territories, recall.
