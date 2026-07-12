"""redteam — adversarial agents that attack the Waggle field, and a scorer
that grades how well the substrate's defenses resist each attack.

Connection Map v2 round 2, #1. Everything up to now assumed honest agents.
This is the attack model: run real hostile agents against a real `waggled`
(started with `-debug` so the attack-metrics endpoint is live), then score
the field on each of three attacks.

    waggled -addr :7777 -debug &
    python3 sdk/redteam/redteam.py            # runs all scenarios, prints scores

Stdlib only, same discipline as sdk/python. Attack agents are just agents
with hostile goals — no new runtime.

The three scenarios and what a passing defense looks like:

  Sybil gold spam   — N synthetic identities flood coordinated `gold` on a
                      dead resource. A ring's deposits are same-origin, not
                      independent corroboration; the field should (a) cap the
                      inflated effective mass via tier weighting — a
                      self-report ring needs 5x the identities to match one
                      on-chain-anchored signal — and (b) surface the ring as a
                      suspected cluster (near-identical intensity, tight time
                      window). PASS = cluster detected AND inflated mass below
                      the single-legit-signal bar per identity.

  Taboo griefing    — an agent spams `taboo` on a legitimate gold path to
                      censor it. At the bare Waggle core level taboo is
                      unauthenticated, so this SUCCEEDS — which is exactly why
                      Èṣù's capability gate exists (Omo-Koda2). PASS here means
                      the attack is *detected* (flooding agent + suspected
                      cluster), quantifying the exposure the gate closes.

  Lease squatting   — an agent claims resources and never releases or works.
                      The field's lease expiry must reclaim within the TTL
                      bound under adversarial hold. PASS = reclaim latency
                      stays within a small multiple of the TTL.
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("WAGGLE_URL", "http://127.0.0.1:7777").rstrip("/")


def _http(method: str, path: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return json.loads(e.read().decode())
        except Exception:
            return {"error": f"HTTP {e.code}"}
    except urllib.error.URLError as e:
        raise SystemExit(f"cannot reach waggled at {BASE}: {e.reason}. "
                         f"Start it with: waggled -addr :7777 -debug") from None


def register(agent_id: str):
    _http("POST", "/v1/agents", {"id": agent_id, "name": agent_id})


def deposit(agent, resource, kind, **kw):
    body = {"agent": agent, "resource": resource, "kind": kind}
    body.update(kw)
    return _http("POST", "/v1/signals", body)


def claim(agent, resource, ttl_s):
    return _http("POST", "/v1/claims", {"agent": agent, "resource": resource, "ttl_s": ttl_s})


def sniff(resource, kind=""):
    q = f"/v1/sniff?resource={resource}"
    if kind:
        q += f"&kind={kind}"
    return _http("GET", q).get("signals", [])


def attack_metrics():
    out = _http("GET", "/v1/debug/attack-metrics")
    if "error" in out:
        raise SystemExit(f"attack metrics unavailable: {out['error']}. "
                         f"Start waggled with -debug.")
    return out


# ── scenario 1: Sybil gold spam ─────────────────────────────────────────────

def scenario_sybil(ring_size=8, resource="repo://dead-end-trap"):
    print(f"\n[sybil] {ring_size} identities flooding self-report gold on {resource}")
    for i in range(ring_size):
        agent = f"sybil-{i}"
        register(agent)
        # coordinated ring: near-identical intensity, no independent judgment
        deposit(agent, resource, "gold", intensity=9.0, note="totally legit, look here")

    # one honest, high-trust signal on a real resource for comparison
    register("honest-oracle")
    deposit("honest-oracle", "repo://real-find", "gold",
            intensity=9.0, evidence_tier="on-chain-anchored")

    ring = [s for s in sniff(resource, "gold")]
    ring_effective = sum(s.get("effective_intensity", 0) for s in ring)
    honest = sniff("repo://real-find", "gold")
    honest_effective = honest[0].get("effective_intensity", 0) if honest else 0

    m = attack_metrics()
    clusters = m.get("suspected_clusters", [])
    detected = any(c["resource"] == resource and len(c["agents"]) >= ring_size - 1
                   for c in clusters)

    # A self-report ring's per-identity effective weight is 9*0.2=1.8; the
    # single anchored signal is 9*1.0=9.0. The ring needs 5 identities just to
    # match one honest signal — tier weighting is doing real work.
    per_identity = ring_effective / max(ring_size, 1)
    passed = detected and per_identity <= honest_effective
    print(f"  ring effective mass:   {ring_effective:.1f} (per identity {per_identity:.2f})")
    print(f"  one honest anchored:   {honest_effective:.1f}")
    print(f"  ring detected as cluster: {detected}")
    print(f"  VERDICT: {'PASS' if passed else 'FAIL'} — "
          f"{'tier weighting caps the ring and the cluster is flagged' if passed else 'ring evaded detection or out-massed honest signal'}")
    return passed


# ── scenario 2: taboo griefing ──────────────────────────────────────────────

def scenario_taboo_grief(resource="repo://legit-path", spam=10):
    print(f"\n[taboo-grief] agent spamming {spam}x taboo to censor {resource}")
    register("honest-worker")
    deposit("honest-worker", resource, "gold", intensity=8.0, evidence_tier="watch-derived")
    gold_before = sniff(resource, "gold")
    before = gold_before[0].get("effective_intensity", 0) if gold_before else 0

    register("griefer")
    for i in range(spam):
        deposit("griefer", resource, "taboo", intensity=10.0, note="censored")

    gold_after = sniff(resource, "gold")
    after = gold_after[0].get("effective_intensity", 0) if gold_after else 0

    m = attack_metrics()
    flooding = [f["agent"] for f in m.get("flooding_agents", [])]
    detected = "griefer" in flooding or any(
        c["kind"] == "taboo" for c in m.get("suspected_clusters", []))

    # At bare-core level the griefer DOES suppress the gold (taboo is
    # unauthenticated here — that's why Èṣù's gate exists). A pass = the
    # exposure is at least detected, quantifying what the gate must close.
    suppression = 1 - (after / before) if before else 0
    passed = detected
    print(f"  gold effective: {before:.2f} -> {after:.2f} ({suppression*100:.0f}% suppressed)")
    print(f"  griefer detected (flooding/cluster): {detected}")
    print(f"  VERDICT: {'PASS' if passed else 'FAIL'} — "
          f"{'attack detected; Èṣù capability gate is the mitigation' if passed else 'griefing went undetected'}")
    if suppression > 0.5:
        print(f"  NOTE: bare core does not authenticate taboo — the capability")
        print(f"        gate in Omo-Koda2 (Èṣù) is what blocks unauthorized taboo.")
    return passed


# ── scenario 3: lease squatting ─────────────────────────────────────────────

def scenario_lease_squat(n=6, ttl_s=2.0):
    print(f"\n[lease-squat] agent claiming {n} resources at ttl={ttl_s}s, never releasing")
    register("squatter")
    for i in range(n):
        r = claim("squatter", f"task://contested-{i}", ttl_s)
        if not r.get("granted"):
            print(f"  unexpected: claim {i} denied")

    # honest agents cannot get the resources while the lease is live...
    register("honest-claimant")
    blocked = claim("honest-claimant", "task://contested-0", ttl_s)
    print(f"  honest claim while squatted: granted={blocked.get('granted')} (expected False)")

    # ...but expiry must reclaim within a bounded time. Wait past the TTL.
    time.sleep(ttl_s + 0.5)
    reclaimed = claim("honest-claimant", "task://contested-0", ttl_s)
    print(f"  honest claim after TTL expiry: granted={reclaimed.get('granted')} (expected True)")

    m = attack_metrics()
    lat = m.get("reclaim_latency_s", {})
    reclaim_max = lat.get("max", 0)
    # PASS: expiry reclaimed AND latency stayed within 2x the TTL bound
    passed = reclaimed.get("granted") and reclaim_max <= ttl_s * 2 + 1
    print(f"  reclaim latency max: {reclaim_max:.2f}s (bound {ttl_s*2+1:.1f}s)")
    print(f"  VERDICT: {'PASS' if passed else 'FAIL'} — "
          f"{'lease expiry reclaims within bound; squatting cannot wedge the field' if passed else 'squat held past the bound'}")
    return passed


SCENARIOS = {
    "sybil": scenario_sybil,
    "taboo-grief": scenario_taboo_grief,
    "lease-squat": scenario_lease_squat,
}


def main(argv):
    which = argv[1] if len(argv) > 1 else "all"
    if which == "all":
        results = {name: fn() for name, fn in SCENARIOS.items()}
    elif which in SCENARIOS:
        results = {which: SCENARIOS[which]()}
    else:
        raise SystemExit(f"unknown scenario {which}; choose: all, {', '.join(SCENARIOS)}")

    print("\n" + "=" * 56)
    passed = sum(1 for v in results.values() if v)
    for name, ok in results.items():
        print(f"  {name:14s} {'PASS' if ok else 'FAIL'}")
    print(f"  {'TOTAL':14s} {passed}/{len(results)} defenses held")
    print("=" * 56)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
