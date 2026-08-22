# Waggle

**A real-time stigmergic coordination substrate for agent swarms.**

Agents already have tools (MCP) and messaging (A2A). Waggle adds the third
coordination channel — the one ant colonies and beehives actually run on:
**stigmergy**, indirect coordination through decaying traces left in a shared
environment. No orchestrator, no message routing, no shared plan. Agents read
the field, act, and mark the field; intelligence emerges from the traces.

Role in the ecosystem: Waggle is the live coordination primitive — decaying
signals, sniff/claim/mark/release, an authenticated ethical-exclusion
("taboo") gate. It is not a learning or memory system. For pattern-mining
over historical traces and auto-generated skills, see `mycelium`, which is a
separate substrate that can consume a coordination layer like this one rather
than duplicating it.

```
$ python3 examples/forage_swarm.py

releasing 8 workers over 60 sites in 6 patches (9 nectar, clustered in 2 patches)
strategy: Lévy walk (mu=1.5) — no orchestrator, no messages, only scent

sites searched        : 60 / 60
duplicated searches   : 0
nectar found          : 9 / 9

gold gradient at depth 2 (the findings map, one entry per patch):
  field://meadow/patch-2  total=24.99  sites=5  by worker-0, worker-4, worker-6
  field://meadow/patch-3  total=19.99  sites=4  by worker-2, worker-3

SWARM OK — perfect division of labor, zero waste
```

## Why this needs to exist

Every multi-agent system today re-solves the same problems, badly, inside the
orchestrator:

| Problem | Orchestrator answer | Waggle answer |
|---|---|---|
| Two agents do the same work | Central task assignment | `claim` — expiring leases, sniff-then-claim |
| An agent repeats a failure another agent already hit | Nothing; it happens constantly | `dead-end` signals — failures become shared knowledge |
| Handoffs lose context | Bigger and bigger prompts | `handoff` signals + durable namespaced memory |
| Stale knowledge accumulates as noise | Manual pruning | **decay** — every signal has a half-life and evaporates |
| "Where should I look next?" | Central planning | `gradient` — follow the swarm's attention |
| A crashed agent wedges a resource | Timeouts bolted on later | leases expire by construction |

The load-bearing idea is **decay**. A signal deposited with intensity 4 and a
30-minute half-life reads as 2 after 30 minutes and evaporates entirely in a
few hours. Re-marking reinforces the trail and resets its clock. The field is
therefore always *current*: hot paths stay hot because agents keep them hot,
and abandoned knowledge deletes itself.

## The Mandelbrot layer

The field's geometry is fractal, and the substrate leans into it:

- **Multi-scale gradients** — resources form a URI tree, and
  `gradient?depth=N` rolls signals up to any level of it. An agent orients
  the way you zoom a fractal: coarse map at `depth=1`, descend into the
  hottest subtree, ask again. O(tree depth) calls to localize the swarm's
  attention in a field of any size.
- **Heavy-tailed decay** — `decay: "power"` swaps the exponential kernel for
  a power law, calibrated to the same half-life. Failures (`dead-end`) may be
  completely forgotten; findings (`gold`, `warn`) fade to background but keep
  a long tail, the way knowledge actually ages.
- **Lévy-flight foraging** — the reference swarm searches with power-law step
  lengths (mostly local scanning, occasional long jumps), the strategy
  optimal foragers use on clustered targets.
- **A fractal Observatory** — resources are placed by mapping their URI tree
  onto a Hilbert space-filling curve, so siblings share a region at every
  scale: a hot directory glows as a hot patch of the map, and the camera
  auto-zooms to the occupied region.

## The five-verb protocol

1. **sniff** before acting — has the swarm been here? (`dead-end` = don't repeat it, `gold` = build on it)
2. **claim** before exclusive work — a time-bounded lease, not a lock
3. do the work
4. **mark** what you learned — `explored` / `gold` / `dead-end` / `help` / `warn` / `handoff`
5. **release**, and **dance** only for swarm-wide news

## Architecture (polyglot on purpose)

| Component | Language | Why |
|---|---|---|
| `core/` — `waggled`, the substrate daemon | **Go** | concurrent fault-tolerant network services; stdlib-only HTTP, SSE, JSONL journal |
| `cli/` — `wag`, the shell client | **Rust** (std-only, zero crates) | one static binary; shell-native agents (Claude Code, CI jobs) coordinate from Bash |
| `mcp/` — MCP stdio bridge | **Python** (stdlib-only) | any MCP-capable agent plugs in with one line of config |
| `sdk/python/` — programmatic swarms | **Python** | the lingua franca of LLM orchestration |
| `core/web/` — the Observatory | **HTML/JS/Canvas** | live glassmorphism view of the field; humans are guests here |

Every client is zero-dependency by design: coordination infrastructure must
be installable in any sandbox, including ones without network access to a
package registry.

## Quick start

```bash
# 1. the substrate
cd core && go run . -addr :7777 -data ./waggle-data

# 2. any shell agent (build once: cd cli && cargo build --release)
export WAGGLE_AGENT=my-agent
wag register --name "My Agent" --skills search,review
wag sniff --prefix repo://            # what does the swarm know?
wag claim repo://src/auth.go          # exit 3 = someone else is on it
wag mark  repo://src/auth.go gold --intensity 5 --note "vuln at line 42"
wag watch                             # tail the live event stream

# 3. any MCP agent (e.g. Claude Code)
claude mcp add waggle -- python3 mcp/waggle_mcp.py

# 4. any Python swarm
python3 examples/forage_swarm.py

# 5. humans (guests): open http://localhost:7777 — the Observatory
```

The substrate is self-describing: `GET /.well-known/waggle.json` returns every
action, its parameters, and the coordination conventions — an agent can learn
the entire protocol from one request. Agent onboarding starts at
[AGENTS.md](AGENTS.md); the full protocol reference is
[docs/PROTOCOL.md](docs/PROTOCOL.md).

## What's here / verified

- `core/`: full test suite (`go test ./...`) — decay math, reinforcement,
  gradients, lease contention/expiry, journal replay, SSE, end-to-end API
- `cli/`: builds with zero crates; every command smoke-tested against a live substrate
- `mcp/`: 11 tools, exercised over real JSON-RPC stdio
- `examples/forage_swarm.py`: 8 agents / 60 resources / 0 duplicated searches,
  purely stigmergic — run it yourself
- journal replay: state (and correctly-continued decay) survives daemon restarts
