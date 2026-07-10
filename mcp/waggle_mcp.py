#!/usr/bin/env python3
"""waggle_mcp — zero-dependency MCP stdio server for the Waggle substrate.

Bridges any MCP-capable agent (Claude Code, Claude Desktop, custom SDK
agents) onto the stigmergic coordination substrate. Python stdlib only:
JSON-RPC 2.0 over stdio on one side, the waggled HTTP API on the other.

Register with Claude Code:

    claude mcp add waggle -- python3 mcp/waggle_mcp.py

Environment:
    WAGGLE_URL    substrate base URL (default http://127.0.0.1:7777)
    WAGGLE_AGENT  default agent identity for tool calls
"""

import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("WAGGLE_URL", "http://127.0.0.1:7777").rstrip("/")
DEFAULT_AGENT = os.environ.get("WAGGLE_AGENT", "")

PROTOCOL_VERSION = "2024-11-05"
SERVER_INFO = {"name": "waggle", "version": "0.1.0"}

KINDS = ["explored", "gold", "dead-end", "help", "warn", "handoff", "claimed", "heartbeat"]

AGENT_PARAM = {
    "type": "string",
    "description": "Acting agent id. Optional if WAGGLE_AGENT is set.",
}

TOOLS = [
    {
        "name": "waggle_register",
        "description": (
            "Create or resume an agent profile on the substrate. Do this once at "
            "session start; a stable id resumes your identity and memory namespace."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "id": {"type": "string", "description": "Stable agent id (resume identity)."},
                "name": {"type": "string"},
                "goals": {"type": "array", "items": {"type": "string"}},
                "skills": {"type": "array", "items": {"type": "string"}},
            },
        },
    },
    {
        "name": "waggle_sniff",
        "description": (
            "Read the scent field BEFORE acting on a resource. Returns live decayed "
            "signals, strongest first. dead-end signals mean another agent already "
            "failed there; gold means a finding worth building on."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "resource": {"type": "string", "description": "Exact resource URI."},
                "prefix": {"type": "string", "description": "Resource URI prefix."},
                "kind": {"type": "string", "enum": KINDS},
                "min": {"type": "number", "description": "Minimum current intensity."},
                "limit": {"type": "integer"},
            },
        },
    },
    {
        "name": "waggle_mark",
        "description": (
            "Deposit a decaying signal on a resource AFTER acting, so successor agents "
            "inherit your experience. Re-marking the same kind reinforces the trail. "
            "Kinds: explored (looked at it), gold (valuable finding), dead-end (tried and "
            "failed, do not repeat), help (stuck, want assistance), warn (hazardous), "
            "handoff (context parked for a successor), heartbeat (liveness)."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "agent": AGENT_PARAM,
                "resource": {"type": "string", "description": "URI: repo://path, task://id, topic://name, https://…"},
                "kind": {"type": "string", "enum": KINDS},
                "intensity": {"type": "number", "description": "0-10, default 1. Use higher for stronger findings."},
                "half_life_s": {"type": "number", "description": "Decay half-life in seconds, default 1800."},
                "decay": {
                    "type": "string", "enum": ["exp", "power"],
                    "description": "Decay kernel. 'exp' (default) forgets completely; 'power' is heavy-tailed — halves at one half-life but fades to background instead of nothing. Use for durable findings (gold, warn).",
                },
                "alpha": {"type": "number", "description": "Power-law exponent, default 1. Higher = faster tail."},
                "note": {"type": "string", "description": "Free text for whoever sniffs this later."},
            },
            "required": ["resource", "kind"],
        },
    },
    {
        "name": "waggle_gradient",
        "description": (
            "Ranked hotspots: where is the swarm's attention right now? Filter "
            "kind=help for a rescue map, kind=gold for a findings map, kind=dead-end "
            "for a map of what to avoid. To orient in a large space, zoom "
            "coarse-to-fine: call with depth=1, descend into the hottest subtree "
            "with depth=2, and so on — O(tree depth) instead of O(resources)."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "prefix": {"type": "string"},
                "kind": {"type": "string", "enum": KINDS},
                "k": {"type": "integer", "description": "Top-k hotspots, default 20."},
                "depth": {"type": "integer", "description": "Roll signals up to this URI-tree level (0=scheme, 1=first path segment, ...). Omit for individual resources."},
            },
        },
    },
    {
        "name": "waggle_claim",
        "description": (
            "Acquire or renew an exclusive time-bounded lease on a resource before "
            "working on it, so no other agent duplicates the work. If another agent "
            "holds it, the response says who — go do something else. Leases expire on "
            "their own, so crashed agents never wedge a resource."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "agent": AGENT_PARAM,
                "resource": {"type": "string"},
                "ttl_s": {"type": "number", "description": "Lease seconds, default 300. Renew before expiry for long work."},
            },
            "required": ["resource"],
        },
    },
    {
        "name": "waggle_release",
        "description": "Release a lease you hold, as soon as the work is done.",
        "inputSchema": {
            "type": "object",
            "properties": {"agent": AGENT_PARAM, "resource": {"type": "string"}},
            "required": ["resource"],
        },
    },
    {
        "name": "waggle_dance",
        "description": (
            "Broadcast to the whole swarm (the bee waggle dance). Use sparingly, for "
            "news every agent should hear now; ambient knowledge belongs in waggle_mark."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "agent": AGENT_PARAM,
                "topic": {"type": "string"},
                "payload": {"type": "object", "description": "Arbitrary JSON payload."},
            },
            "required": ["topic"],
        },
    },
    {
        "name": "waggle_listen",
        "description": "Poll swarm broadcasts after a sequence cursor. Returns dances and the next cursor.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "since": {"type": "integer", "description": "Last seen sequence number, 0 for all retained."},
                "topic": {"type": "string"},
                "limit": {"type": "integer"},
            },
        },
    },
    {
        "name": "waggle_remember",
        "description": (
            "Store a JSON value in durable namespaced memory. Your profile's namespace "
            "(agent/<id>) is yours; shared namespaces like swarm/plan are by convention."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "namespace": {"type": "string"},
                "key": {"type": "string"},
                "value": {"description": "Any JSON value."},
            },
            "required": ["namespace", "key", "value"],
        },
    },
    {
        "name": "waggle_recall",
        "description": "Fetch a value from durable memory, or list a namespace's keys if key is omitted.",
        "inputSchema": {
            "type": "object",
            "properties": {"namespace": {"type": "string"}, "key": {"type": "string"}},
            "required": ["namespace"],
        },
    },
    {
        "name": "waggle_swarm",
        "description": "Who is on the substrate: agent profiles (goals, skills, last seen) plus live claims and status.",
        "inputSchema": {"type": "object", "properties": {}},
    },
]


# ---- substrate HTTP client -----------------------------------------------------


def http(method, path, body=None, params=None):
    url = BASE + path
    if params:
        url += "?" + urllib.parse.urlencode({k: v for k, v in params.items() if v not in (None, "")})
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return json.loads(e.read().decode())
        except Exception:
            return {"error": f"substrate returned HTTP {e.code}"}
    except urllib.error.URLError as e:
        return {"error": f"cannot reach substrate at {BASE}: {e.reason}. Is waggled running?"}


def need_agent(args):
    agent = args.get("agent") or DEFAULT_AGENT
    if not agent:
        raise ValueError("no agent identity: pass 'agent' or set WAGGLE_AGENT")
    return agent


def call_tool(name, args):
    if name == "waggle_register":
        return http("POST", "/v1/agents", body=args)
    if name == "waggle_sniff":
        return http("GET", "/v1/sniff", params=args)
    if name == "waggle_mark":
        body = dict(args)
        body["agent"] = need_agent(args)
        return http("POST", "/v1/signals", body=body)
    if name == "waggle_gradient":
        return http("GET", "/v1/gradient", params=args)
    if name == "waggle_claim":
        return http("POST", "/v1/claims", body={
            "agent": need_agent(args), "resource": args["resource"],
            "ttl_s": args.get("ttl_s", 0)})
    if name == "waggle_release":
        return http("POST", "/v1/claims/release", body={
            "agent": need_agent(args), "resource": args["resource"]})
    if name == "waggle_dance":
        return http("POST", "/v1/dances", body={
            "agent": need_agent(args), "topic": args["topic"],
            "payload": args.get("payload", {})})
    if name == "waggle_listen":
        out = http("GET", "/v1/dances", params=args)
        dances = out.get("dances", [])
        if dances:
            out["next_since"] = max(d["seq"] for d in dances)
        return out
    if name == "waggle_remember":
        return http("PUT", f"/v1/memory/{args['namespace']}/{args['key']}", body=args["value"])
    if name == "waggle_recall":
        if args.get("key"):
            return http("GET", f"/v1/memory/{args['namespace']}/{args['key']}")
        return http("GET", f"/v1/memory/{args['namespace']}", params={"keys": "1"})
    if name == "waggle_swarm":
        return {
            "agents": http("GET", "/v1/agents").get("agents", []),
            "claims": http("GET", "/v1/claims").get("claims", []),
            "status": http("GET", "/v1/status"),
        }
    raise ValueError(f"unknown tool: {name}")


# ---- MCP (JSON-RPC 2.0 over stdio) ----------------------------------------------


def reply(id_, result=None, error=None):
    msg = {"jsonrpc": "2.0", "id": id_}
    if error is not None:
        msg["error"] = error
    else:
        msg["result"] = result
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def handle(msg):
    method = msg.get("method", "")
    id_ = msg.get("id")
    params = msg.get("params") or {}

    if method == "initialize":
        reply(id_, {
            "protocolVersion": params.get("protocolVersion", PROTOCOL_VERSION),
            "capabilities": {"tools": {}},
            "serverInfo": SERVER_INFO,
            "instructions": (
                "Waggle is a stigmergic coordination substrate for agent swarms. "
                "Protocol: (1) waggle_register once; (2) waggle_sniff before acting "
                "on any shared resource; (3) waggle_claim before exclusive work; "
                "(4) do the work; (5) waggle_mark what you learned (gold/dead-end/"
                "explored) and waggle_release. Check waggle_gradient to see where "
                "the swarm's attention is, and waggle_listen for broadcasts."
            ),
        })
    elif method == "notifications/initialized":
        pass  # notification, no reply
    elif method == "tools/list":
        reply(id_, {"tools": TOOLS})
    elif method == "tools/call":
        name = params.get("name", "")
        args = params.get("arguments") or {}
        try:
            result = call_tool(name, args)
            is_err = isinstance(result, dict) and set(result.keys()) == {"error"}
            reply(id_, {
                "content": [{"type": "text", "text": json.dumps(result, indent=2)}],
                "isError": is_err,
            })
        except Exception as e:  # tool errors are results, not protocol errors
            reply(id_, {"content": [{"type": "text", "text": str(e)}], "isError": True})
    elif method == "ping":
        reply(id_, {})
    elif id_ is not None:
        reply(id_, error={"code": -32601, "message": f"method not found: {method}"})


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        try:
            handle(msg)
        except Exception as e:
            if msg.get("id") is not None:
                reply(msg["id"], error={"code": -32603, "message": str(e)})


if __name__ == "__main__":
    main()
