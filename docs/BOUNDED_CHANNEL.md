# The `bounded` channel — decay kernel math (spec)

Status: **design spec, not yet implemented.** Nothing here is in the manifest
yet; per the house rule, the capability does not exist until it ships in the
Go handlers, `core/manifest.go`, the CLI, the MCP tools, and `PROTOCOL.md`
together. This document is the math those five changes implement.

Context: Connection Map v2 (`docs/CONNECTION_MAP_V2.md` in the Omo-Koda2
repo; §0.7, §5, §8) promotes Mandelbrot fragility analysis from a LOOM/Axiom demo
to a shared analytical primitive. Every producer — ỌṢỌVM's build-time
perturbation gate, LOOM's Fractal Oracle verdicts, Ṣàngó's anchored
robustness claims, Vantage's imported foreign classifications — writes to
this one channel, with this one kernel, instead of inventing parallel
fragility math.

## 1. What a `bounded` signal carries

A `bounded` signal is a **verdict about robustness**, not a discovery event.
That difference drives every choice below.

The oracle's primitive is escape time under `z ← z² + c`: a probe that stays
bounded through `maxiter` iterations is a robust island; one that escapes
early is brittle. The oracle already normalizes this to a **stability score**

```
s = escape_time / maxiter        ∈ [0, 1]
```

(`1.0` = deep bounded island, `→0` = escapes immediately; see
`Axiom/agents/oracle/src/lib.rs`, `classify()`).

Mapping onto the existing signal shape:

| field         | value                                                        |
|---------------|--------------------------------------------------------------|
| `kind`        | `"bounded"`                                                  |
| `intensity`   | `10 · s` — stability on the field's native 0–10 scale        |
| `half_life_s` | default **7200** (4× the gold default of 1800)               |
| `decay`       | `"power"` (heavy tail — always; see §2)                      |
| `alpha`       | stability-dependent, set by the depositor per §2             |
| `meta`        | `{escape, maxiter, verdict, probe}` — the raw oracle output so any reader (or auditor) can recompute `s` |

Reusing `intensity` as the carrier means `sniff`, `gradient`, and the
Observatory need **zero new read-path math**: a hot `bounded` gradient *is* a
robust region, a cold one *is* fragile or unknown. The only read-path caveat
is that "absent" and "fragile" both read as low — consumers that need the
distinction (LOOM's dead-cat-bounce filter) check for the signal's existence
via `sniff`, not just the gradient number.

## 2. The kernel: confidence-weighted heavy tail

Both existing kernels halve at exactly one half-life — that invariant is the
contract (`half_life_s` means the same thing everywhere) and this channel
keeps it. `bounded` always uses the power-law kernel already in
`core/field.go`:

```
I(t) = I₀ · (1 + t/scale)^(-α),   scale = half_life_s / (2^(1/α) − 1)
```

The new rule is that **α is a function of the stability score**:

```
α(s) = α_max − (α_max − α_min) · s
       with α_min = 0.5, α_max = 2.0

s = 1.0  (deep island)   → α = 0.5   — very heavy tail
s = 0.5  (boundary)      → α = 1.25
s = 0.0  (instant escape)→ α = 2.0   — fastest tail the channel allows
```

Why this shape:

- **Robustness findings should persist — that's what "robust" means.** A
  deep-bounded verdict is a claim about the *structure* of a region of
  parameter space, and structure changes slowly. At 10 half-lives (20 hours
  at the default), an exponential signal is at 0.1%; power-law α=1 at ~9%;
  α=0.5 at ~22%. A confirmed island stays visible as background scent for
  days without reinforcement.
- **Fragility verdicts are cheaper to regenerate and more likely to churn**,
  so they earn the fast end of the range. But even α=2 is heavier-tailed
  than exponential — a "this was brittle" reading fading to background beats
  it vanishing, because a *re-appearing* fragile verdict is itself signal.
- Both endpoints still halve at one half-life, so decay-rate anomaly
  detection (§4) has a fixed baseline to compare against regardless of `s`.

Depositors compute `α(s)` client-side and pass it in the existing `alpha`
field — the daemon's kernel needs no change for this part. What *does*
change in the daemon is registration of the channel defaults in the
manifest's new `channels` block (Connection Map §0.2) so SDKs pick them up
without hardcoding.

## 3. Reinforcement: replace, not add

The existing rule — same `(agent, resource, kind)` deposit adds to the
decayed value, capped at 10 — is right for pheromones and wrong for
verdicts. Two independent `s = 0.3` scans must not sum to a reading of
`s = 0.6`: the region did not get more robust because it was measured twice.

The channel registration therefore carries a `reinforce` mode, the first
divergence from global deposit semantics:

```json
{"channel": "bounded", "reinforce": "replace", ...}
```

Under `replace`, a re-deposit by the same `(agent, resource, kind)` **sets**
intensity to the new `10 · s`, resets the decay clock, and re-derives `α`.
The journal keeps both entries (append-only as ever), so `recall` still sees
the full verdict history — which §4 depends on.

Deposits from *different* agents on the same resource remain separate
signals, exactly as today. Agreement between them is expressed through the
`evidence_tier` ladder, not through summation: a verdict corroborated by a
second agent's independent scan is promoted a tier, which multiplies its
read-time weight (Connection Map §0.8):

```
tier weights: self-report 0.2 · corroborated 0.4 · watch-derived 0.6
              zangbeto-verified 0.8 · on-chain-anchored 1.0
```

`sniff`/`gradient` apply the weight at read time; stored intensity stays
pure so promotion never rewrites history.

## 4. Regime-shift detection (LOOM §5.4)

Decay is lazy and deterministic, so a signal can never decay "faster than
configured" on its own — apparent acceleration can only come from
**replacement deposits with falling `s`**. That makes the leading indicator
cheap and unambiguous. With two `recall` samples at times `t₁ < t₂`:

```
RSI = (s(t₁) − s(t₂)) / Δt · half_life_s
```

i.e. stability lost per half-life. Thresholds: `RSI > 0.5` — the ground
under this strategy is destabilizing at better than half a stability unit
per half-life — is the early warning; `RSI > 1.0` is an active regime
break. Note what this is *not*: it never looks at PnL or any lagging outcome
measure. It reads only the field's own verdict history, which is why it
leads instead of lags.

## 5. Cross-inhibition: the dead-cat-bounce filter (§5.7)

`bounded` cross-inhibits `gold` — a win reported inside known-brittle
territory gets down-weighted, not deleted. When a resource (or its rollup
group at the queried depth) carries a live `bounded` signal with stability
`s`, gold read there is scaled by

```
m(s) = max(0.25, min(1, s / 0.5))
```

- `s ≥ 0.5` (boundary or better): no suppression, `m = 1`.
- `s → 0` (deep escape zone): gold reads at a floor of 25% — skepticism,
  never erasure, so a genuinely persistent gold source can still climb back
  through reinforcement.
- No `bounded` signal present: `m = 1`. Absence of a robustness verdict is
  not evidence of fragility.

The multiplier applies at read time in `sniff`/`gradient` and is reported by
`sniff_explain` (Connection Map §0.9) as a named contribution, so an agent
staring at a suspiciously cold gold reading can see exactly which `bounded`
verdict is suppressing it and at what tier.

## 6. Defaults, in one place

```json
{
  "channel": "bounded",
  "default_half_life_s": 7200,
  "decay_kernel": "power",
  "alpha_range": [0.5, 2.0],
  "reinforce": "replace",
  "cross_inhibits": [
    {"channel": "gold", "ref": 0.5, "floor": 0.25}
  ]
}
```

This is the object the `channels` block of `/.well-known/waggle.json`
publishes; the Python SDK, `wag`, and the MCP tools read it rather than
embedding any constant above.

## 7. Worked example

ỌṢỌVM compiles agent bytecode, perturbation-tests it (12 input variations,
`maxiter`-equivalent budget from the shared depth convention — see
`FRACTAL_ORACLE.md` §5), and gets `escape = 96, maxiter = 120 → s = 0.8`:

- deposit: `intensity 8.0`, `alpha(0.8) = 0.8`, `half_life_s 7200`, tier
  `watch-derived` (auto-deposited off the compile log).
- Read immediately: `8.0 × 0.6 (tier) = 4.8` effective.
- Zangbeto verifies the receipt → tier promotes to `zangbeto-verified`:
  same stored intensity now reads `8.0 × 0.8 = 6.4`.
- 20 hours (10 half-lives) later, unreinforced: `(1 + t/scale)^{-0.8}` has
  it at ~13% of deposit — still faintly visible, exactly the "fades to
  background, not to nothing" behavior gold pioneered, only heavier.
- A rebuild scores `s = 0.3` on the same URI. Replace-mode deposit drops the
  reading to 3.0; two `recall` samples put `RSI ≈ (0.8 − 0.3)/Δt ·
  half_life` well past 0.5 — the regression gate (Connection Map §1.10)
  blocks the ship and points at the diff.
