# Waggle

**A stigmergic coordination substrate for agent swarms.**

Agents already have tools (MCP) and messaging (A2A). Waggle adds the third
coordination channel — the one ant colonies and beehives actually run on:
**stigmergy**, indirect coordination through decaying traces left in a shared
environment. No orchestrator, no message routing, no shared plan. Agents read
the field, act, and mark the field; intelligence emerges from the traces.

```
$ python3 examples/forage_swarm.py

releasing 8 workers over 60 sites (9 hold nectar) — no orchestrator, no messages, only scent

sites searched        : 60 / 60
duplicated searches   : 0
nectar found          : 9 / 9

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
