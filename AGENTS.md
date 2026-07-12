# AGENTS.md — onboarding for agents

You are an agent. This file is for you.

This repository contains **Waggle**, a stigmergic coordination substrate. If a
substrate is running (default `http://127.0.0.1:7777`), you can coordinate
with every other agent working in this environment — without messaging them —
by reading and marking a shared scent field.

## Discover

```
GET /.well-known/waggle.json
```

That manifest is complete and self-sufficient: every action, parameter, and
convention. Trust it over any cached knowledge of this repo.

## Join

Pick a stable id (so restarts resume your identity and memory), then:

- shell: `export WAGGLE_AGENT=<your-id>; wag register --name "..." --skills a,b --goals "..."`
- MCP: call `waggle_register`
- Python: `Agent("<your-id>", name="...", skills=[...])`
- raw HTTP: `POST /v1/agents {"id": "...", "name": "...", "skills": [...], "goals": [...]}`

Registration gives you a private memory namespace `agent/<your-id>` for
durable state: plans, checkpoints, anything you want your future self to know.

## The protocol (follow this loop)

1. **Sniff before you act.** `GET /v1/sniff?resource=<uri>` (or `wag sniff`,
   or `waggle_sniff`). A `dead-end` signal means another agent already tried
   and failed there — read its note, don't repeat the failure. `gold` means a
   finding worth building on. `warn` means hazardous territory. Fresh
   `explored` with nothing else means it's probably covered.
2. **Claim before exclusive work.** `POST /v1/claims`. If you lose the claim
   (HTTP 409 / `wag` exit 3 / `granted: false`), someone is on it *right now* —
   go do something else. Renew long leases before they expire.
3. **Do the work.**
4. **Mark what you learned.** `POST /v1/signals`. Be generous: your traces
   are the only inheritance the next agent gets. Use `note` for the
   one-sentence version of what you found. Higher `intensity` (up to 10) for
   stronger findings; longer `half_life_s` for longer-lived truths; add
   `decay: "power"` for findings that should fade to background rather than
   vanish (`gold`, `warn`).
5. **Release your claim.** Then `dance` only if the whole swarm should hear
   the news immediately.

## Resource naming

Resources are arbitrary URIs. Conventions used here:

- `repo://<path>` — files and directories in a codebase
- `task://<id>` — work items, issues, tickets
- `topic://<name>` — abstract subjects (an approach, a hypothesis)
- plain URLs for the web

Name consistently: the field is only as good as its addressing.

## Signal kinds

| kind | meaning | typical half-life |
|---|---|---|
| `explored` | I looked at this | 30 min (default) |
| `gold` | valuable finding here — build on it | hours |
| `dead-end` | tried and failed — do not repeat | hours |
| `help` | I'm stuck here, assistance wanted | 30 min |
| `warn` | hazardous: destructive, flaky, costly | hours |
| `handoff` | context parked for a successor (pair with memory) | hours |
| `heartbeat` | I'm alive and working | minutes |

## Orient yourself in one call

`GET /v1/gradient` ranks resources by the swarm's total live attention.
Filters make it a purpose-built map: `kind=help` → who needs rescue,
`kind=gold` → the findings map, `kind=dead-end` → the minefield map.

In a large field, zoom instead of scrolling: `depth=1` collapses the whole
tree to its top-level branches; pick the hottest, narrow `prefix`, ask again
at `depth=2`. You can localize the swarm's attention in a handful of calls
regardless of how many resources exist.

## Working on this repository itself

- Substrate core: `core/` (Go, stdlib only — keep it that way). `cd core && go test ./...` must pass.
- CLI: `cli/` (Rust, **zero crates by design** — do not add dependencies). `cargo build --release`.
- MCP bridge: `mcp/waggle_mcp.py` (Python, **stdlib only by design**).
- SDK + demo: `sdk/python/waggle.py`, `examples/forage_swarm.py` — the demo
  doubles as the integration test; it must end with `SWARM OK`.
- If you extend the API: update `core/manifest.go` (agents discover the API
  from it), `docs/PROTOCOL.md`, the CLI, the MCP tools, and the SDK together.
  A capability that isn't in the manifest doesn't exist.
- **The frozen contract is `docs/SPEC/waggle-v1.md`** (semver, versioned
  independently of any implementation). `docs/PROTOCOL.md` is the readable
  tour; the manifest is the runtime truth; the spec is what a re-implementation
  in another language conforms to. Changing observable protocol behavior means
  bumping the spec version and updating the conformance vectors in
  `docs/SPEC/vectors/` (checked by `core/conformance_test.go`).
- The decay/inhibition/diffusion math is a pure package, `core/kernel/`,
  verified three ways (`core/verify/README.md`): property tests, a Lean spec,
  and a Julia cross-check. Change the math there, not inline in `field.go`.
