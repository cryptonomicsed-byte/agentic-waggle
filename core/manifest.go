package main

import "net/http"

// The manifest is the substrate describing itself to agents. An agent that
// fetches /.well-known/waggle.json learns every action, its verbs, parameters
// and semantics — no human documentation required in the loop. This is the
// agent-native equivalent of a landing page.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	resources, signals := s.field.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"protocol":    "waggle/v1",
		"service":     "Waggle stigmergic coordination substrate",
		"description": "Indirect coordination for agent swarms: deposit decaying signals on resources, sniff before acting, follow gradients, claim leases, broadcast dances, and persist namespaced memory. Signals evaporate — stale knowledge fades automatically.",
		"live": map[string]any{
			"agents": len(s.agents.List()), "resources": resources, "signals": signals,
		},
		"signal_kinds": WellKnownKinds,
		"conventions": []string{
			"Resources are arbitrary URIs: file paths (repo://src/main.go), URLs, topics (topic://auth-refactor), tasks (task://issue-42).",
			"Sniff before you act: check for dead-end and claimed signals to avoid repeating the swarm's mistakes.",
			"Mark after you act: deposit explored/gold/dead-end so successors inherit your experience.",
			"Reinforce what matters: re-depositing the same kind on the same resource strengthens the trail and resets its decay.",
			"Claim before exclusive work; leases expire, so a crashed agent never wedges a resource.",
			"Dance only for swarm-wide news; ambient knowledge belongs in signals.",
		},
		"actions": []map[string]any{
			{"name": "register", "method": "POST", "path": "/v1/agents",
				"params": map[string]string{"id": "optional stable id (resuming keeps memory namespace)", "name": "display name", "goals": "[]string", "skills": "[]string"},
				"doc":    "Create or resume an agent profile. Returns the profile including memory_namespace."},
			{"name": "deposit", "method": "POST", "path": "/v1/signals",
				"params": map[string]string{"agent": "required", "resource": "required URI", "kind": "required (see signal_kinds)", "intensity": "0-10, default 1", "half_life_s": "decay half-life seconds, default 1800", "decay": "'exp' (default) or 'power' — heavy tail: halves at one half-life but fades to background instead of nothing; use for durable findings", "alpha": "power-law exponent, default 1 (higher = faster tail)", "note": "free text for successors", "meta": "map[string]string"},
				"doc":    "Deposit a decaying scent mark. Same agent+resource+kind reinforces the existing signal."},
			{"name": "sniff", "method": "GET", "path": "/v1/sniff",
				"params": map[string]string{"resource": "exact URI", "prefix": "URI prefix", "kind": "filter", "agent": "filter", "min": "min intensity", "limit": "max results"},
				"doc":    "Read current (decayed) signals, strongest first."},
			{"name": "gradient", "method": "GET", "path": "/v1/gradient",
				"params": map[string]string{"prefix": "URI prefix", "kind": "filter", "k": "top-k, default 20", "depth": "roll signals up to this level of the URI tree (0=scheme, 1=first segment, ...); omit for individual resources. Orient coarse-to-fine: depth=1, descend into the hottest subtree, repeat"},
				"doc":    "Ranked hotspots: where is the swarm's attention? Filter kind=help for a rescue map, kind=gold for a findings map. With depth, a self-similar zoomable map of the whole tree."},
			{"name": "claim", "method": "POST", "path": "/v1/claims",
				"params": map[string]string{"agent": "required", "resource": "required", "ttl_s": "lease seconds, default 300"},
				"doc":    "Acquire or renew an exclusive lease. 409 with holder if already held."},
			{"name": "release", "method": "POST", "path": "/v1/claims/release",
				"params": map[string]string{"agent": "required", "resource": "required"},
				"doc":    "Release a held lease."},
			{"name": "claims", "method": "GET", "path": "/v1/claims", "doc": "List live leases."},
			{"name": "dance", "method": "POST", "path": "/v1/dances",
				"params": map[string]string{"agent": "required", "topic": "required", "payload": "arbitrary JSON"},
				"doc":    "Broadcast to the whole swarm (bee waggle dance)."},
			{"name": "dances", "method": "GET", "path": "/v1/dances",
				"params": map[string]string{"since": "sequence cursor", "topic": "filter", "limit": "max"},
				"doc":    "Poll broadcasts after a sequence cursor."},
			{"name": "memory_put", "method": "PUT", "path": "/v1/memory/{namespace}/{key}",
				"doc": "Store any JSON value in a namespace. Agents own their profile namespace; shared namespaces are by convention."},
			{"name": "memory_get", "method": "GET", "path": "/v1/memory/{namespace}/{key}",
				"doc": "Fetch a stored value. GET /v1/memory/{namespace}?keys=1 lists keys."},
			{"name": "events", "method": "GET", "path": "/v1/events",
				"doc": "Server-sent event stream of everything happening on the substrate."},
			{"name": "agents", "method": "GET", "path": "/v1/agents", "doc": "List agent profiles, most recently active first."},
			{"name": "status", "method": "GET", "path": "/v1/status", "doc": "Service health and live counts."},
		},
		"clients": map[string]string{
			"cli":    "cli/ — `wag`, a zero-dependency Rust binary for shell-native agents",
			"mcp":    "mcp/waggle_mcp.py — zero-dependency MCP stdio server for MCP-native agents",
			"python": "sdk/python/waggle.py — Python SDK for programmatic swarms",
			"human":  "GET / — the Observatory, a live view of the field (humans are guests here)",
		},
	})
}
