#!/usr/bin/env python3
"""Foraging swarm demo: emergent division of labor with no orchestrator.

Eight worker agents share one job: search 60 sites for hidden nectar. Nobody
assigns work. Nobody talks to anybody. Each worker follows the stigmergic
protocol against the substrate:

    1. sniff a candidate site — skip it if it smells explored or dead-end
    2. claim it — skip if another worker holds the lease
    3. "search" it (simulated work)
    4. mark the outcome: gold (nectar!) or dead-end, plus explored
    5. release the lease; dance when nectar is found

The demo then prints the receipts: every site searched exactly once, zero
duplicated work, all nectar found — coordination that emerged purely from
traces in the shared field.

Run it (starts against an already-running substrate):

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

SITES = [f"field://meadow/site-{i:02d}" for i in range(60)]
NECTAR = set(random.sample(SITES, 9))  # hidden treasure, unknown to workers
WORKERS = 8

lock = threading.Lock()
searched: list[str] = []          # every completed search, with duplicates if any
found: set[str] = set()


def forage(worker_no: int) -> None:
    me = Agent(f"worker-{worker_no}", name=f"Worker {worker_no}",
               skills=["forage"], goals=["find all nectar"])
    idle_passes = 0
    while idle_passes < 3:
        progress = False
        sites = SITES[:]
        random.shuffle(sites)  # no assigned order — the field is the schedule
        for site in sites:
            # 1. sniff: has the swarm already been here?
            if me.sniff(resource=site):
                continue
            # 2. claim: is someone here right now?
            if not me.claim(site, ttl_s=30):
                continue
            try:
                # re-check after winning the race: a finisher may have marked
                # between our sniff and our claim
                if any(s["agent"] != me.id for s in me.sniff(resource=site)):
                    continue
                # 3. the actual work
                time.sleep(random.uniform(0.01, 0.05))
                has_nectar = site in NECTAR
                # 4. mark the outcome for every future forager
                me.mark(site, "explored", half_life_s=3600)
                if has_nectar:
                    me.mark(site, "gold", intensity=5, half_life_s=3600,
                            note="nectar here")
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
        idle_passes = 0 if progress else idle_passes + 1


def main() -> None:
    try:
        Agent("queen", name="Queen", skills=["observe"])
    except SubstrateError as e:
        print(f"error: {e}", file=sys.stderr)
        print("start the substrate first:  cd core && go run . -addr :7777", file=sys.stderr)
        sys.exit(1)

    print(f"releasing {WORKERS} workers over {len(SITES)} sites "
          f"({len(NECTAR)} hold nectar) — no orchestrator, no messages, only scent\n")
    t0 = time.time()
    threads = [threading.Thread(target=forage, args=(i,)) for i in range(WORKERS)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    elapsed = time.time() - t0

    dupes = len(searched) - len(set(searched))
    observer = Agent("queen")
    gold_map = observer.gradient(prefix="field://", kind="gold", k=20)

    print(f"sites searched        : {len(set(searched))} / {len(SITES)}")
    print(f"duplicated searches   : {dupes}")
    print(f"nectar found          : {len(found)} / {len(NECTAR)}")
    print(f"wall time             : {elapsed:.2f}s across {WORKERS} workers")
    print("\ngold gradient (the swarm's shared findings map):")
    for h in gold_map:
        print(f"  {h['resource']}  intensity={h['total']:.2f}  by {', '.join(h['agents'])}")

    ok = (len(set(searched)) == len(SITES) and dupes == 0 and found == NECTAR)
    print(f"\n{'SWARM OK — perfect division of labor, zero waste' if ok else 'SWARM DEGRADED — see numbers above'}")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
