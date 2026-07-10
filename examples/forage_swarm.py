#!/usr/bin/env python3
"""Foraging swarm demo: emergent division of labor with no orchestrator.

Eight worker agents share one job: search a meadow of 60 sites (6 patches x
10 sites) for hidden nectar. The nectar is CLUSTERED — all of it sits in two
patches — because that is what real search spaces look like: findings clump.

Nobody assigns work and nobody messages anybody. Each worker runs a Lévy
walk over the meadow (many short local hops, occasional long jumps — the
heavy-tailed search strategy optimal foragers use on patchy targets) and
follows the stigmergic protocol at every site:

    1. sniff — skip sites that smell explored or dead-end
    2. claim — skip sites another worker holds
    3. "search" it (simulated work)
    4. mark the outcome: gold uses the heavy-tailed power-law decay kernel
       (findings should fade to background, not to nothing); dead-ends decay
       exponentially (failures can be fully forgotten)
    5. release; dance when nectar is found

The demo prints the receipts — every site searched exactly once, zero
duplicated work, all nectar found — and then reads the field back at patch
scale with a depth-limited gradient: the swarm's findings map, one hotspot
per patch, straight out of the substrate.

Run it (against an already-running substrate):

    cd core && go run . -addr :7777 &
    python3 examples/forage_swarm.py
"""

import os
import random
import sys
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "sdk", "python"))
from waggle import Agent, SubstrateError  # noqa: E402

PATCHES = 6
SITES_PER_PATCH = 10
SITES = [f"field://meadow/patch-{p}/site-{i:02d}"
         for p in range(PATCHES) for i in range(SITES_PER_PATCH)]
N = len(SITES)

# nectar clumps: two patches hold all of it
NECTAR_PATCHES = random.sample(range(PATCHES), 2)
NECTAR = {SITES[p * SITES_PER_PATCH + i]
          for p in NECTAR_PATCHES
          for i in random.sample(range(SITES_PER_PATCH), 5 if p == NECTAR_PATCHES[0] else 4)}

WORKERS = 8
LEVY_MU = 1.5  # Pareto tail exponent; ~2.0 is optimal for sparse revisitable targets

lock = threading.Lock()
searched: list[str] = []
found: set[str] = set()
step_log: list[int] = []


def levy_step() -> int:
    """Heavy-tailed step length: mostly 1-2, occasionally huge."""
    step = int(random.random() ** (-1.0 / LEVY_MU))
    return max(1, min(step, N // 2))


def forage(worker_no: int) -> None:
    me = Agent(f"worker-{worker_no}", name=f"Worker {worker_no}",
               skills=["forage"], goals=["find all nectar"])
    p = random.randrange(N)         # random drop point in the meadow
    visits_since_progress = 0
    while visits_since_progress < 3 * N:
        site = SITES[p]
        progress = False
        # 1. sniff: has the swarm already been here?
        if not me.sniff(resource=site):
            # 2. claim: is someone here right now?
            if me.claim(site, ttl_s=30):
                try:
                    # re-check after winning the race: a finisher may have
                    # marked between our sniff and our claim
                    if not any(s["agent"] != me.id for s in me.sniff(resource=site)):
                        # 3. the actual work
                        time.sleep(random.uniform(0.01, 0.05))
                        has_nectar = site in NECTAR
                        # 4. mark the outcome for every future forager
                        me.mark(site, "explored", half_life_s=3600)
                        if has_nectar:
                            # findings keep a long tail; failures may be forgotten
                            me.mark(site, "gold", intensity=5, half_life_s=3600,
                                    decay="power", note="nectar here")
                            me.dance("nectar-found", {"site": site})
                        else:
                            me.mark(site, "dead-end", half_life_s=3600)
                        with lock:
                            searched.append(site)
                            if has_nectar:
                                found.add(site)
                        progress = True
                finally:
                    # 5. clean up the lease
                    me.release(site)
        visits_since_progress = 0 if progress else visits_since_progress + 1
        # Lévy walk: local scanning with occasional long relocations
        step = levy_step()
        with lock:
            step_log.append(step)
        p = (p + random.choice((-1, 1)) * step) % N


def main() -> None:
    try:
        Agent("queen", name="Queen", skills=["observe"])
    except SubstrateError as e:
        print(f"error: {e}", file=sys.stderr)
        print("start the substrate first:  cd core && go run . -addr :7777", file=sys.stderr)
        sys.exit(1)

    print(f"releasing {WORKERS} workers over {N} sites in {PATCHES} patches "
          f"({len(NECTAR)} nectar, clustered in 2 patches)")
    print(f"strategy: Lévy walk (mu={LEVY_MU}) — no orchestrator, no messages, only scent\n")
    t0 = time.time()
    threads = [threading.Thread(target=forage, args=(i,)) for i in range(WORKERS)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    elapsed = time.time() - t0

    dupes = len(searched) - len(set(searched))
    long_jumps = sum(1 for s in step_log if s > 5)
    print(f"sites searched        : {len(set(searched))} / {N}")
    print(f"duplicated searches   : {dupes}")
    print(f"nectar found          : {len(found)} / {len(NECTAR)}")
    print(f"wall time             : {elapsed:.2f}s across {WORKERS} workers")
    print(f"walk profile          : {len(step_log)} steps, "
          f"{long_jumps} long jumps (>{5}), max {max(step_log)}")

    # read the field back at patch scale: one call, one hotspot per patch
    observer = Agent("queen")
    print("\ngold gradient at depth 2 (the findings map, one entry per patch):")
    for h in observer.gradient(prefix="field://", kind="gold", k=10, depth=2):
        print(f"  {h['resource']}  total={h['total']:.2f}  "
              f"sites={h['resources']}  by {', '.join(sorted(h['agents']))}")

    ok = (len(set(searched)) == N and dupes == 0 and found == NECTAR)
    print(f"\n{'SWARM OK — perfect division of labor, zero waste' if ok else 'SWARM DEGRADED — see numbers above'}")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
