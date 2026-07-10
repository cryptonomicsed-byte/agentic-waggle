"""waggle — Python SDK for the Waggle stigmergic coordination substrate.

Stdlib only. One class, `Agent`, wrapping the substrate's HTTP API with the
coordination protocol baked in: sniff before acting, claim before exclusive
work, mark after acting.

    from waggle import Agent

    scout = Agent("scout-1", name="Scout", skills=["search"], goals=["find nectar"])
    if scout.sniff(resource="repo://src/auth.go", kind="dead-end"):
        ...  # someone already failed here; pick another target
    if scout.claim("repo://src/auth.go"):
        try:
            ...  # exclusive work
            scout.mark("repo://src/auth.go", "gold", intensity=5, note="vuln at line 42")
        finally:
            scout.release("repo://src/auth.go")
"""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.parse
import urllib.request


class SubstrateError(RuntimeError):
    """The substrate rejected a request or is unreachable."""


class Agent:
    """An agent's handle on the substrate, bound to a registered profile."""

    def __init__(self, agent_id: str = "", *, name: str = "", goals: list[str] | None = None,
                 skills: list[str] | None = None, base_url: str = ""):
        self.base = (base_url or os.environ.get("WAGGLE_URL", "http://127.0.0.1:7777")).rstrip("/")
        profile = self._http("POST", "/v1/agents", body={
            "id": agent_id, "name": name,
            "goals": goals or [], "skills": skills or [],
        })
        self.id: str = profile["id"]
        self.name: str = profile["name"]
        self.memory_namespace: str = profile["memory_namespace"]

    # ---- field ----------------------------------------------------------

    def mark(self, resource: str, kind: str, *, intensity: float = 1.0,
             half_life_s: float = 0, note: str = "") -> dict:
        """Deposit a decaying signal; re-marking the same kind reinforces it."""
        return self._http("POST", "/v1/signals", body={
            "agent": self.id, "resource": resource, "kind": kind,
            "intensity": intensity, "half_life_s": half_life_s, "note": note})

    def sniff(self, *, resource: str = "", prefix: str = "", kind: str = "",
              agent: str = "", min_intensity: float = 0, limit: int = 0) -> list[dict]:
        """Current (decayed) signals, strongest first."""
        return self._http("GET", "/v1/sniff", params={
            "resource": resource, "prefix": prefix, "kind": kind,
            "agent": agent, "min": min_intensity or "", "limit": limit or ""})["signals"]

    def gradient(self, *, prefix: str = "", kind: str = "", k: int = 0) -> list[dict]:
        """Ranked hotspots: where is the swarm's attention?"""
        return self._http("GET", "/v1/gradient", params={
            "prefix": prefix, "kind": kind, "k": k or ""})["hotspots"]

    # ---- claims ----------------------------------------------------------

    def claim(self, resource: str, ttl_s: float = 0) -> bool:
        """Try to acquire/renew an exclusive lease. False means someone else holds it."""
        try:
            out = self._http("POST", "/v1/claims", body={
                "agent": self.id, "resource": resource, "ttl_s": ttl_s})
        except SubstrateError as e:
            if getattr(e, "status", 0) == 409:
                return False
            raise
        return bool(out.get("granted"))

    def release(self, resource: str) -> bool:
        return bool(self._http("POST", "/v1/claims/release", body={
            "agent": self.id, "resource": resource}).get("released"))

    # ---- dances ----------------------------------------------------------

    def dance(self, topic: str, payload: dict | None = None) -> dict:
        """Broadcast swarm-wide news."""
        return self._http("POST", "/v1/dances", body={
            "agent": self.id, "topic": topic, "payload": payload or {}})

    def listen(self, since: int = 0, topic: str = "", limit: int = 0) -> list[dict]:
        return self._http("GET", "/v1/dances", params={
            "since": since, "topic": topic, "limit": limit or ""})["dances"]

    # ---- memory ----------------------------------------------------------

    def remember(self, key: str, value, namespace: str = "") -> None:
        ns = namespace or self.memory_namespace
        self._http("PUT", f"/v1/memory/{ns}/{key}", body=value)

    def recall(self, key: str, namespace: str = "", default=None):
        ns = namespace or self.memory_namespace
        try:
            return self._http("GET", f"/v1/memory/{ns}/{key}")["value"]
        except SubstrateError as e:
            if getattr(e, "status", 0) == 404:
                return default
            raise

    # ---- plumbing ----------------------------------------------------------

    def _http(self, method: str, path: str, *, body=None, params=None):
        url = self.base + path
        if params:
            clean = {k: v for k, v in params.items() if v not in (None, "")}
            if clean:
                url += "?" + urllib.parse.urlencode(clean)
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return json.loads(resp.read().decode())
        except urllib.error.HTTPError as e:
            payload = {}
            try:
                payload = json.loads(e.read().decode())
            except Exception:
                pass
            err = SubstrateError(payload.get("error", f"HTTP {e.code}"))
            err.status = e.code
            err.payload = payload
            raise err from None
        except urllib.error.URLError as e:
            raise SubstrateError(
                f"cannot reach substrate at {self.base}: {e.reason}. Is waggled running?") from None
