"""ecosystem_scale — the whole swarm, at load, under attack, in one harness.

Connection Map v2 round 2, #3. The property tests prove the kernel is correct
in isolation and redteam proves the defenses work against a single attacker on
a quiet field. Neither answers the question an operator actually has: does the
substrate hold up when all seven powers are hammering it *concurrently* and a
fraction of the traffic is hostile? Throughput, tail latency, and — the part
that matters — does correctness survive contention: does honest anchored gold
still out-mass a Sybil ring, does cost-efficiency ranking still hold, do leases
still reclaim, when the field is hot?

This is a synthetic-swarm generator (the load Yemọja's spawner would produce)
pointed at a real `waggled`. Start the daemon with -debug so the correctness
phase can read attack metrics, and with -data if you also want to feed Axiom /
the digest from the same run:

    cd core && go run . -addr :7777 -debug &
    python3 benchmarks/ecosystem_scale/scale.py --powers-per 6 --duration 8 \
        --adversary-frac 0.2

Point Axiom (or `wag watch`, or the Observatory at GET /) at the same daemon to
*watch* the storm live while it runs.

Stdlib only — threads, urllib, statistics. The adversary threads reuse the
redteam attack model verbatim (sdk/redteam) rather than reimplementing it.
"""

from __future__ import annotations

import argparse
import json
import os
import random
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import defaultdict

# reuse the real attack agents + scorers rather than a second copy
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "sdk", "redteam"))
import redteam  # noqa: E402

BASE = os.environ.get("WAGGLE_URL", "http://127.0.0.1:7777").rstrip("/")


def _http(method: str, path: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read().decode())


# ── thread-safe metrics ─────────────────────────────────────────────────────

class Metrics:
    def __init__(self):
        self.lock = threading.Lock()
        self.lat_ms: dict[str, list[float]] = defaultdict(list)  # op -> latencies
        self.errors = 0

    def record(self, op: str, ms: float):
        with self.lock:
            self.lat_ms[op].append(ms)

    def error(self):
        with self.lock:
            self.errors += 1

    def total_ops(self) -> int:
        return sum(len(v) for v in self.lat_ms.values())


def timed(metrics: Metrics, op: str, fn):
    t0 = time.perf_counter()
    try:
        fn()
    except (urllib.error.URLError, urllib.error.HTTPError, OSError):
        metrics.error()
        return
    metrics.record(op, (time.perf_counter() - t0) * 1000)


# ── the seven powers ────────────────────────────────────────────────────────
#
# Each power is a worker body run in a loop by `worker_count` threads for the
# duration. Resources overlap within a power (contention) and the honest
# populations never touch the resources the correctness phase plants on.

def power_osovm(m: Metrics, rng: random.Random):
    """Build/compile: gold on compiled units, cost = compile wall-clock."""
    unit = rng.randrange(64)
    timed(m, "deposit", lambda: _http("POST", "/v1/signals", {
        "agent": f"osovm-{threading.get_ident()}", "resource": f"osovm://unit/{unit}",
        "kind": "gold", "intensity": 3, "note": "bytecode cached",
        "cost": {"wall_clock_ms": rng.uniform(5, 400),
                 "source": {"producer": "osovm", "method": "compile-wall-clock", "units": "ms"}}}))


def power_loom(m: Metrics, rng: random.Random):
    """Trades: gold on wins / dead-end on losses, cost = commission+latency."""
    preset, market = rng.randrange(12), rng.choice(["btc", "eth", "sol"])
    win = rng.random() < 0.55
    timed(m, "deposit", lambda: _http("POST", "/v1/signals", {
        "agent": f"loom-{threading.get_ident()}", "resource": f"loom://p{preset}/{market}",
        "kind": "gold" if win else "dead-end", "intensity": rng.uniform(1, 6),
        "cost": {"dollars": rng.uniform(0.01, 3.0), "wall_clock_ms": rng.uniform(50, 800),
                 "source": {"producer": "loom", "method": "commission+slippage+decision-latency",
                            "units": "dollars+ms"}}}))


def power_vantage(m: Metrics, rng: random.Random):
    """Federation: warn signals carrying a cross-ecosystem robustness divergence."""
    r = rng.randrange(20)
    timed(m, "deposit", lambda: _http("POST", "/v1/signals", {
        "agent": f"vantage-{threading.get_ident()}", "resource": f"repo://shared/{r}",
        "kind": "warn", "intensity": rng.uniform(1, 4),
        "meta": {"remote_field": "partner-x", "local_stability": f"{rng.random():.2f}",
                 "remote_stability": f"{rng.random():.2f}"}}))


def power_ifscript(m: Metrics, rng: random.Random):
    """Casts: bounded verdicts (intensity = stability*10) on divined resources."""
    r = rng.randrange(24)
    stability = rng.random()
    timed(m, "deposit", lambda: _http("POST", "/v1/signals", {
        "agent": f"ifa-{threading.get_ident()}", "resource": f"ori://cast/{r}",
        "kind": "bounded", "intensity": round(stability * 10, 2),
        "meta": {"verdict": "robust" if stability >= 0.5 else "fragile"}}))


def power_zangbeto(m: Metrics, rng: random.Random):
    """Verification: high-trust gold at the zangbeto-verified evidence tier."""
    r = rng.randrange(16)
    timed(m, "deposit", lambda: _http("POST", "/v1/signals", {
        "agent": f"zangbeto-{threading.get_ident()}", "resource": f"chain://anchor/{r}",
        "kind": "gold", "intensity": rng.uniform(4, 9),
        "evidence_tier": rng.choice(["zangbeto-verified", "on-chain-anchored"])}))


def power_omokoda(m: Metrics, rng: random.Random):
    """Gated agents: explored trails plus reads before acting."""
    r = rng.randrange(30)
    timed(m, "deposit", lambda: _http("POST", "/v1/signals", {
        "agent": f"omokoda-{threading.get_ident()}", "resource": f"task://work/{r}",
        "kind": "explored", "intensity": rng.uniform(1, 3)}))
    timed(m, "sniff", lambda: _http("GET", f"/v1/sniff?prefix=task://work/{r}"))


def power_axiom(m: Metrics, rng: random.Random):
    """The observer: read-heavy, never deposits. Gradient + sniff, like the galaxy."""
    depth = rng.choice([1, 2])
    timed(m, "gradient", lambda: _http("GET", f"/v1/gradient?depth={depth}&weighted=true&k=20"))
    timed(m, "sniff", lambda: _http("GET", "/v1/sniff?prefix=repo://&limit=25"))


POWERS = {
    "osovm": power_osovm, "loom": power_loom, "vantage": power_vantage,
    "ifscript": power_ifscript, "zangbeto": power_zangbeto,
    "omokoda": power_omokoda, "axiom": power_axiom,
}


def run_worker(body, m: Metrics, stop: threading.Event, seed: int):
    rng = random.Random(seed)
    while not stop.is_set():
        body(m, rng)


def run_adversary(kind: str, m: Metrics, stop: threading.Event):
    """Hostile traffic mixed into the load — reuses the redteam attack agents.
    Runs its scenario repeatedly (fresh resource each pass) until time is up."""
    n = 0
    while not stop.is_set():
        try:
            if kind == "sybil":
                # a lighter ring than the scorer's, so it can repeat under the clock
                res = f"repo://sybil-trap/{n}"
                for i in range(6):
                    a = f"adv-sybil-{n}-{i}"
                    redteam.register(a)
                    redteam.deposit(a, res, "gold", intensity=9.0, note="look here")
                    m.record("deposit", 0)
            elif kind == "taboo":
                res = f"repo://grief/{n}"
                redteam.register("adv-griefer")
                for _ in range(8):
                    redteam.deposit("adv-griefer", res, "taboo", intensity=10.0)
                    m.record("deposit", 0)
            n += 1
        except (urllib.error.URLError, urllib.error.HTTPError, OSError):
            m.error()
        time.sleep(0.05)


# ── correctness under load: run the real redteam checks on the hot field ─────

def correctness_phase(snapshot_capture: dict | None = None) -> tuple[dict, dict]:
    """After the storm, the substrate's guarantees must still hold. These plant
    their own isolated resources, so they measure the field's behavior under the
    residual load, not the synthetic traffic itself.

    Two tiers, because they are not equally strong promises:

      invariants  — hard substrate guarantees that MUST survive load: tier
                    weighting caps a Sybil ring, lease expiry reclaims within
                    bound, cost-efficiency ranking is stable. These gate the
                    verdict.
      observations — best-effort signals we report but do NOT gate on. Taboo-
                    grief *detection* is rate-relative (an agent above 5x the
                    median rate); in a busy field the median is high, so a lone
                    griefer hides in the crowd. That degradation is a finding,
                    not a regression — it is exactly why the real mitigation is
                    Èṣù's authenticated-taboo capability gate (Omo-Koda2), not
                    field-level anomaly detection.

    The taboo result moves between tiers depending on the daemon: when the
    daemon runs Èṣù's gate in enforce mode (-taboo-auth-enforce), an
    unauthenticated griefer taboo is *refused at deposit*, so taboo-grief
    resistance becomes a hard invariant — this is the close-out for the finding
    above. Without the gate it stays the best-effort observation."""
    import io, contextlib
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):  # silence redteam's own prints
        invariants = {
            "sybil_capped_and_flagged": redteam.scenario_sybil(ring_size=8),
            "lease_reclaims_in_bound": redteam.scenario_lease_squat(n=5, ttl_s=2.0),
            "cost_efficiency_ranking_holds": cost_efficiency_holds(),
            # cross-inhibition math (kernel property tests prove it in isolation)
            # must also hold against real concurrent multi-channel writes
            "cross_inhibition_never_amplifies": inhibition_bounded_under_load(),
        }
        # a snapshot captured mid-storm must be a clean, verifiable read
        if snapshot_capture is not None and snapshot_capture.get("captured"):
            invariants["snapshot_restores_under_load"] = snapshot_restores(snapshot_capture)
        observations = {}
        if taboo_gate_enforced():
            # the gate is live: taboo-grief resistance is now a hard invariant
            invariants["taboo_grief_blocked_by_gate"] = taboo_grief_blocked()
        else:
            observations["taboo_grief_detected"] = redteam.scenario_taboo_grief(spam=10)
    return invariants, observations


def _taboo_refused(agent: str, resource: str) -> bool:
    """Deposit an unauthenticated taboo and report whether the daemon refused it
    with 403 (Èṣù enforce mode). Uses a status-aware request rather than
    redteam._http, which collapses the HTTP code into the JSON error body."""
    body = json.dumps({"agent": agent, "resource": resource, "kind": "taboo",
                       "intensity": 10.0}).encode()
    req = urllib.request.Request(BASE + "/v1/signals", data=body, method="POST",
                                 headers={"Content-Type": "application/json"})
    try:
        urllib.request.urlopen(req, timeout=10)
        return False  # accepted → gate not enforcing
    except urllib.error.HTTPError as e:
        return e.code == 403
    except urllib.error.URLError:
        return False


def taboo_gate_enforced() -> bool:
    """Probe whether the daemon enforces Èṣù's taboo gate: an unauthenticated
    taboo is refused (403) iff enforce mode is on."""
    redteam.register("gate-probe")
    return _taboo_refused("gate-probe", "probe://gate-check")


def taboo_grief_blocked() -> bool:
    """With the gate enforced, a griefer holding no capability cannot suppress a
    legitimate gold path: every unauthenticated taboo is refused and the gold's
    effective mass is untouched. This is the finding's close-out — the attack is
    blocked at deposit, not merely (and unreliably) detected after the fact."""
    res = "probe://gated-legit-path"
    redteam.register("gold-worker")
    redteam.deposit("gold-worker", res, "gold", intensity=8.0, evidence_tier="watch-derived")
    g = redteam.sniff(res, "gold")
    before = g[0].get("effective_intensity", 0) if g else 0
    redteam.register("gate-griefer")
    refused = sum(_taboo_refused("gate-griefer", res) for _ in range(10))
    g = redteam.sniff(res, "gold")
    after = g[0].get("effective_intensity", 0) if g else 0
    return refused == 10 and before > 0 and after >= before * 0.99


def cost_efficiency_holds() -> bool:
    """A cheap gold and an expensive gold of equal strength, deposited into the
    loaded field — cost_efficiency ranking must still put cheap first."""
    redteam.register("cost-probe")
    redteam.deposit("cost-probe", "probe://cheap", "gold", intensity=5,
                    cost={"tokens": 1000, "dollars": 0.01})
    redteam.deposit("cost-probe", "probe://pricey", "gold", intensity=5,
                    cost={"tokens": 500000, "dollars": 2.5})
    out = _http("GET", "/v1/sniff?prefix=probe://&optimize=cost_efficiency")
    sigs = out.get("signals", [])
    return len(sigs) >= 2 and sigs[0]["resource"] == "probe://cheap"


# ── load-hardening: the two things the first benchmark didn't test ───────────

def inhibition_bounded_under_load() -> bool:
    """Cross-inhibition math, checked against real concurrent multi-channel
    writes rather than the property tests' single-threaded generation. The
    invariant: inhibition only ever *suppresses* — every signal's effective
    intensity stays ≤ decayed × tier-weight (multiplier ≤ 1), and every
    individual inhibition multiplier is ≤ 1. A co-located gold + fragile bounded
    verifies suppression actually fires (bounded's low-mode gold inhibition, the
    dead-cat filter); a field-wide scan over storm resources confirms nothing,
    anywhere, amplified under the concurrent write pressure."""
    eps = 1e-6

    def never_amplifies(ex: dict) -> bool:
        for c in ex.get("contributions", []):
            ceiling = c["signal"].get("intensity", 0) * c.get("tier_weight", 1) * (1 + eps)
            if c.get("effective", 0) > ceiling:
                return False
            for tr in c.get("inhibitions", []):
                if tr.get("multiplier", 1) > 1 + eps:
                    return False
        return True

    # co-located inhibitor + inhibited (gate-independent: no taboo needed)
    res = "probe://inhibition"
    redteam.register("inh-probe")
    redteam.deposit("inh-probe", res, "gold", intensity=8.0)
    redteam.deposit("inh-probe", res, "bounded", intensity=1.5)  # fragile → suppresses gold
    ex = _get_explain(res)
    if not never_amplifies(ex):
        return False
    suppressed = any(
        c.get("effective", 0) < c["signal"].get("intensity", 0) * c.get("tier_weight", 1) * (1 - eps)
        for c in ex.get("contributions", [])
    )

    # field-wide: nothing amplified anywhere under the storm's residual load
    for r in _sample_storm_resources():
        if not never_amplifies(_get_explain(r)):
            return False
    return suppressed


def _get_explain(resource: str) -> dict:
    import urllib.parse
    return _http("GET", "/v1/explain?resource=" + urllib.parse.quote(resource, safe="")) or {}


def _sample_storm_resources(limit: int = 12) -> list[str]:
    """A spread of resources the storm actually wrote, across powers/channels."""
    seen: list[str] = []
    for prefix in ("repo://", "osovm://", "task://", "ori://", "chain://", "loom://"):
        out = _http("GET", f"/v1/sniff?prefix={prefix}&limit=4")
        for s in (out or {}).get("signals", []):
            if s["resource"] not in seen:
                seen.append(s["resource"])
    return seen[:limit]


def snapshot_restores(cap: dict) -> bool:
    """A snapshot taken mid-storm must be a clean, atomic read: loading it back
    the daemon recomputes the content hash and accepts it only if it matches. A
    torn read (a write interleaved into the capture) would produce a snapshot
    whose hash fails to verify. So: the mid-load capture loads with a
    server-verified hash equal to the captured one, and the same signal count."""
    snap = cap.get("snapshot")
    if not snap:
        return False
    out = _http("POST", "/v1/snapshot/load", snap)
    if not isinstance(out, dict) or "error" in out:
        return False
    return out.get("hash") == snap.get("hash") and out.get("loaded") == len(snap.get("signals", []))


# ── report ──────────────────────────────────────────────────────────────────

def pct(xs, p):
    if not xs:
        return 0.0
    xs = sorted(xs)
    k = min(len(xs) - 1, int(round((p / 100) * (len(xs) - 1))))
    return xs[k]


def main(argv):
    ap = argparse.ArgumentParser(description="Waggle 7-power concurrent adversarial benchmark")
    ap.add_argument("--powers-per", type=int, default=5, help="worker threads per power (x7)")
    ap.add_argument("--duration", type=float, default=6.0, help="load phase seconds")
    ap.add_argument("--adversary-frac", type=float, default=0.15,
                    help="hostile threads as a fraction of honest workers")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--json", action="store_true", help="emit machine-readable JSON summary too")
    args = ap.parse_args(argv[1:])

    # preflight: daemon reachable, -debug live
    try:
        _http("GET", "/v1/status")
    except Exception as e:
        raise SystemExit(f"cannot reach waggled at {BASE}: {e}. "
                         f"Start it with: waggled -debug")
    if "error" in _http("GET", "/v1/debug/attack-metrics"):
        raise SystemExit("attack metrics unavailable — start waggled with -debug")

    m = Metrics()
    stop = threading.Event()
    threads: list[threading.Thread] = []

    honest = len(POWERS) * args.powers_per
    for pi, (name, body) in enumerate(POWERS.items()):
        for w in range(args.powers_per):
            t = threading.Thread(target=run_worker,
                                 args=(body, m, stop, args.seed + pi * 100 + w), daemon=True)
            threads.append(t)
    n_adv = max(0, round(honest * args.adversary_frac))
    for i in range(n_adv):
        kind = "sybil" if i % 2 == 0 else "taboo"
        threads.append(threading.Thread(target=run_adversary, args=(kind, m, stop), daemon=True))

    # capture a snapshot mid-storm, racing real concurrent writes: a torn read
    # would produce a hash the daemon later refuses to load. Skips cleanly if the
    # daemon has no journal (-data). Requires -data to exercise.
    snapshot_capture: dict = {"captured": False}

    def snapshot_midload():
        time.sleep(args.duration * 0.5)
        try:
            snap = _http("GET", "/v1/snapshot?prefix=osovm://")
        except (urllib.error.URLError, urllib.error.HTTPError, OSError):
            return  # no -data (409) → snapshot-under-load test is N/A this run
        if isinstance(snap, dict) and "signals" in snap and snap.get("hash"):
            snapshot_capture.update(snapshot=snap, captured=True)

    threads.append(threading.Thread(target=snapshot_midload, daemon=True))

    print(f"ecosystem_scale: {len(POWERS)} powers x {args.powers_per} = {honest} honest "
          f"workers + {n_adv} adversaries, {args.duration:.0f}s against {BASE}")
    t_start = time.perf_counter()
    for t in threads:
        t.start()
    time.sleep(args.duration)
    stop.set()
    for t in threads:
        t.join(timeout=5)
    elapsed = time.perf_counter() - t_start

    print("\n── load ──")
    ops = m.total_ops()
    print(f"  {ops} ops in {elapsed:.1f}s = {ops / elapsed:,.0f} ops/s "
          f"({m.errors} errors)")
    for op in sorted(m.lat_ms):
        xs = m.lat_ms[op]
        print(f"  {op:9s} n={len(xs):6d}  p50={pct(xs,50):6.1f}ms  "
              f"p95={pct(xs,95):6.1f}ms  p99={pct(xs,99):6.1f}ms")

    print("\n── invariants under load (gate the verdict) ──")
    invariants, observations = correctness_phase(snapshot_capture)
    for name, ok in invariants.items():
        print(f"  [{'PASS' if ok else 'FAIL'}] {name}")

    if "taboo_grief_blocked_by_gate" in invariants:
        print("  (Èṣù taboo gate enforced — taboo-grief resistance is a hard invariant here)")
    if observations:
        print("\n── observations (best-effort, reported not gated) ──")
        for name, ok in observations.items():
            note = "" if ok else "  (rate-based detection washes out under load — Èṣù's gate is the fix)"
            print(f"  [{'seen' if ok else 'MISSED'}] {name}{note}")

    all_ok = all(invariants.values())
    err_rate = m.errors / max(ops + m.errors, 1)
    healthy = all_ok and err_rate < 0.01
    print(f"\nRESULT: {'ECOSYSTEM OK' if healthy else 'ECOSYSTEM DEGRADED'} "
          f"— invariants {'held' if all_ok else 'BROKE'} under load, "
          f"error rate {err_rate*100:.2f}%")

    if args.json:
        print(json.dumps({
            "ops": ops, "elapsed_s": elapsed, "ops_per_s": ops / elapsed,
            "errors": m.errors, "error_rate": err_rate,
            "latency_ms": {op: {"p50": pct(xs, 50), "p95": pct(xs, 95), "p99": pct(xs, 99)}
                           for op, xs in m.lat_ms.items()},
            "invariants": invariants, "observations": observations, "healthy": healthy,
        }, indent=2))

    return 0 if healthy else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
