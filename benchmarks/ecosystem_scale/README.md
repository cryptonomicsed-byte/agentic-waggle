# ecosystem_scale — 7-power concurrent adversarial benchmark

Connection Map v2, round 2, #3.

The property tests (`core/kernel`) prove the decay/inhibition math is correct in
isolation. `sdk/redteam` proves the defenses work against a single attacker on a
quiet field. Neither answers the operator's real question:

> When all seven powers are hammering the substrate at once and a fraction of
> the traffic is hostile, does it hold — throughput, tail latency, and above all
> does *correctness survive contention*?

This harness is a synthetic-swarm generator (the load Yemọja's spawner would
produce) pointed at a real `waggled`. It runs the seven ecosystem powers as
concurrent worker populations, mixes in a configurable fraction of hostile
traffic drawn from the redteam attack model, measures load, then re-runs the
substrate's guarantees on the hot field.

## Running

```bash
cd core && go run . -addr :7777 -debug &     # -debug: attack-metrics endpoint
python3 benchmarks/ecosystem_scale/scale.py --powers-per 6 --duration 8 \
    --adversary-frac 0.2 --json
```

`-debug` is required (the correctness phase reads `/v1/debug/attack-metrics`).
Add `-data` if you also want to feed the digest or a snapshot from the same run.
Point Axiom, `wag watch`, or the Observatory (`GET /`) at the same daemon to
**watch** the storm live.

Flags: `--powers-per N` (threads per power, ×7), `--duration S`,
`--adversary-frac F` (hostile threads as a fraction of honest workers),
`--seed`, `--json`.

## The seven powers

| power    | traffic                                                        |
|----------|----------------------------------------------------------------|
| osovm    | `gold` on compiled units, cost = compile wall-clock            |
| loom     | `gold`/`dead-end` on trades, cost = commission + latency       |
| vantage  | `warn` carrying cross-ecosystem robustness divergences         |
| ifscript | `bounded` verdicts (intensity = stability × 10)                |
| zangbeto | high-trust `gold` at `zangbeto-verified` / `on-chain-anchored` |
| omokoda  | `explored` trails + sniff-before-act reads                     |
| axiom    | the observer: `gradient` + `sniff`, never deposits             |

## What it gates on

Two tiers, because they are not equally strong promises:

**Invariants** (gate the verdict — must survive load):
- `sybil_capped_and_flagged` — tier weighting caps a self-report ring's mass
  and the ring is flagged as a cluster.
- `lease_reclaims_in_bound` — lease expiry reclaims a squatted resource within
  a small multiple of the TTL.
- `cost_efficiency_ranking_holds` — a cheap gold still outranks an equally
  strong expensive gold under `optimize=cost_efficiency`.

**Observations** (reported, not gated):
- `taboo_grief_detected` — flooding detection is rate-relative (an agent above
  5× the median deposit rate). In a busy field the median is high, so a lone
  griefer hides in the crowd and detection *washes out*. This degradation is a
  finding, not a regression: it is precisely why the real mitigation is Èṣù's
  authenticated-taboo capability gate (Omo-Koda2), not field-level anomaly
  detection. The benchmark quantifies the exposure the gate closes.

Exit code is 0 for `ECOSYSTEM OK` (all invariants held, error rate < 1%), 1 for
`ECOSYSTEM DEGRADED`.

## Reference numbers

On the CI-class box this was built on, `--powers-per 6 --duration 6`
(42 honest + 8 adversary threads) sustains ~1,400 ops/s at ~29 ms p50 /
~57 ms p95 with zero errors, all invariants holding. Treat these as a smoke
baseline, not a spec — they exist to catch regressions, not to certify a SLA.
