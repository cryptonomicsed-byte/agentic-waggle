# redteam — attacking the field to earn trust in it

Everything else in Waggle assumes honest agents. This is the attack model:
real hostile agents run against a real `waggled`, and the substrate's defenses
are *scored* on how well they resist. Infrastructure other systems depend on
(Ṣàngó anchoring value, LOOM trading real money) should be red-teamed before it
goes live, not after.

## Run

```bash
cd core && go run . -addr :7777 -debug &      # -debug exposes attack metrics
python3 sdk/redteam/redteam.py                # all scenarios, detailed scoring
# or, shell-native with a CI exit code:
cli/target/release/wag attack-sim all
```

## The three attacks and their defenses

| Attack | What it does | Defense scored | Result (live) |
|--------|--------------|----------------|---------------|
| **Sybil gold spam** | N identities flood coordinated `gold` on a dead resource | Tier weighting caps a self-report ring's effective mass (needs 5× the identities to match one on-chain-anchored signal); the attack-metrics cluster detector flags the ring (near-identical intensity, tight window) | **PASS** — ring per-identity mass 1.8 vs 9.0 for one honest anchored signal; ring flagged |
| **Taboo griefing** | An agent spams `taboo` to censor a legit gold path | Bare core does *not* authenticate taboo — the attack succeeds at core level, which is *why* Èṣù's capability gate exists. Pass = the flood is detected, quantifying the exposure the gate closes | **PASS** (detected) — 90% gold suppression at bare core; Èṣù's `CapabilityGate` blocks unauthorized taboo, and `suspected_rings` flags same-origin minting |
| **Lease squatting** | An agent claims resources and never releases/works | Lease expiry must reclaim within the TTL bound under adversarial hold | **PASS** — reclaim latency 2.5s within the 2×TTL bound; honest claim denied while live, granted after expiry |

## Where each defense lives

- **Tier weighting + cluster detection**: Waggle core (`core/kernel` tier
  weights; `core/debug.go` `AttackMetrics`, `/v1/debug/attack-metrics`).
- **Same-origin Sybil detection**: Èṣù's `CapabilityGate::suspected_rings`
  (Omo-Koda2 `omokoda-core/src/waggle`) — the field sees N independent agents;
  Èṣù sees they were all minted from one path in one burst.
- **Lease expiry**: Waggle core `Claims` — expiry is guaranteed by the
  protocol (`docs/SPEC/waggle-v1.md` §10).

The metrics endpoint is off unless `-debug` is passed, so production carries no
adversarial-observability overhead.
