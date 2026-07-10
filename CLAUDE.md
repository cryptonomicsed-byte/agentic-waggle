# CLAUDE.md

Waggle: a stigmergic coordination substrate for agent swarms. Read
[AGENTS.md](AGENTS.md) first — it is the agent-facing onboarding and applies
to you.

## Build & test

```bash
cd core && go test ./... && gofmt -l .        # must pass, must print nothing
cd cli && cargo build --release               # zero crates — never add dependencies
python3 examples/forage_swarm.py              # integration test; must end "SWARM OK"
                                              # (needs: cd core && go run . -addr :7777 & )
```

## Hard constraints

- `core/` is Go **stdlib only**. `cli/` is Rust **std only** (empty
  `[dependencies]`). `mcp/` and `sdk/python/` are Python **stdlib only**.
  Zero-dependency clients are the point: they must run in any sandbox.
- API changes ship in four places together: Go handlers, `core/manifest.go`,
  the CLI, and the MCP tools — plus `docs/PROTOCOL.md`. The manifest is how
  agents discover capabilities; an unlisted capability doesn't exist.
- Decay is computed lazily from `deposited_at` (see `Signal.At`); never add a
  background loop that rewrites intensities. Journal replay depends on
  original timestamps.
- The Observatory (`core/web/index.html`) is one self-contained file: no
  external scripts, fonts, or build step.
