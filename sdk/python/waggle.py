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
import random
import threading
import urllib.error
import urllib.parse
import urllib.request


class SubstrateError(RuntimeError):
    """The substrate rejected a request or is unreachable."""


class Agent:
    """An agent's handle on the substrate, bound to a registered profile."""

    def __init__(self, agent_id: str = "", *, name: str = "", goals: list[str] | None = None,
                 skills: list[str] | None = None,
                 response_thresholds: dict[str, float] | None = None,
                 base_url: str = ""):
        self.base = (base_url or os.environ.get("WAGGLE_URL", "http://127.0.0.1:7777")).rstrip("/")
        profile = self._http("POST", "/v1/agents", body={
            "id": agent_id, "name": name,
            "goals": goals or [], "skills": skills or [],
            "response_thresholds": response_thresholds or {},
        })
        self.id: str = profile["id"]
        self.name: str = profile["name"]
        self.memory_namespace: str = profile["memory_namespace"]
        self.response_thresholds: dict[str, float] = profile.get("response_thresholds") or {}

    # ---- field ----------------------------------------------------------

    def mark(self, resource: str, kind: str, *, intensity: float = 1.0,
             half_life_s: float = 0, decay: str = "", alpha: float = 0,
             subtype: str = "", evidence_tier: str = "",
             cost: dict | None = None,
             note: str = "", meta: dict[str, str] | None = None) -> dict:
        """Deposit a decaying signal; re-marking the same kind reinforces it.

        decay="power" uses a heavy-tailed kernel: still halves at one
        half-life, but fades to background instead of nothing — for durable
        findings (gold, warn). alpha (default 1) steepens the tail.

        Typed channels (see channels()) supply defaults and semantics: a
        "bounded" mark gets the 2h half-life, confidence-weighted tail and
        replace-mode reinforcement without any of these arguments.
        evidence_tier places the mark on the trust ladder (default
        self-report; watch-derived and better come from instruments, not
        arguments).
        """
        body = {
            "agent": self.id, "resource": resource, "kind": kind,
            "intensity": intensity, "half_life_s": half_life_s,
            "decay": decay, "alpha": alpha, "subtype": subtype,
            "evidence_tier": evidence_tier, "note": note, "meta": meta or {}}
        if cost:
            # {tokens, wall_clock_ms, dollars} — what producing this cost;
            # accumulates on reinforcement, drives cost_efficiency ranking
            body["cost"] = cost
        return self._http("POST", "/v1/signals", body=body)

    def sniff(self, *, resource: str = "", prefix: str = "", kind: str = "",
              agent: str = "", min_intensity: float = 0, min_tier: str = "",
              optimize: str = "", limit: int = 0) -> list[dict]:
        """Current (decayed) signals, strongest first.

        min_tier filters by evidence tier: min_tier="corroborated" drops
        unverified self-reports — use before acting where bad scent is
        expensive (e.g. committing capital). Each signal carries
        effective_intensity: decay x tier weight x cross-inhibition.

        optimize="cost_efficiency" ranks by effective intensity per unit
        cost instead of raw strength — prefer a cheap gold over an equally
        strong gold that cost 10k tokens.
        """
        return self._http("GET", "/v1/sniff", params={
            "resource": resource, "prefix": prefix, "kind": kind,
            "agent": agent, "min": min_intensity or "", "min_tier": min_tier,
            "optimize": optimize, "limit": limit or ""})["signals"]

    def sniff_batch(self, uris: list[str], *, kind: str = "",
                    weighted: bool = False) -> dict[str, dict]:
        """Gradient rollups for many URIs in one call.

        Each URI is a subtree prefix; returns {uri: hotspot}. A depth-first
        explorer prices N candidate branches for one round-trip instead of N.
        """
        return self._http("POST", "/v1/sniff/batch", body={
            "uris": uris, "kind": kind, "weighted": weighted})["results"]

    def gradient(self, *, prefix: str = "", kind: str = "", k: int = 0,
                 depth: int = -1, weighted: bool = False,
                 diffuse: bool = False) -> list[dict]:
        """Ranked hotspots: where is the swarm's attention?

        depth >= 0 rolls signals up to that URI-tree level (0=scheme,
        1=first segment, ...) for a coarse-to-fine zoomable view; the
        default ranks individual resources. weighted=True sums trust-adjusted
        intensities (evidence tier x cross-inhibition); diffuse=True adds the
        5% sibling bleed at leaf level.
        """
        return self._http("GET", "/v1/gradient", params={
            "prefix": prefix, "kind": kind, "k": k or "",
            "depth": depth if depth >= 0 else "",
            "weighted": "1" if weighted else "", "diffuse": "1" if diffuse else ""})["hotspots"]

    def explain(self, resource: str) -> dict:
        """Why does this resource read the way it does?

        Every live signal with its evidence tier weight, the
        cross-inhibitions suppressing it, effective intensity, and the
        ambient diffusion from siblings.
        """
        return self._http("GET", "/v1/explain", params={"resource": resource})

    def recall_at(self, at: str, *, resource: str = "", prefix: str = "",
                  kind: str = "", agent: str = "", min_intensity: float = 0,
                  limit: int = 0) -> list[dict]:
        """The field as it stood at a past RFC3339 instant (journal replay).

        Sniff answers "what does the field say now"; recall_at answers "what
        did it say then" — intensities decay to the recalled instant, and
        history includes the faded. Requires the daemon to run with -data.
        """
        return self._http("GET", "/v1/recall", params={
            "at": at, "resource": resource, "prefix": prefix, "kind": kind,
            "agent": agent, "min": min_intensity or "", "limit": limit or ""})["signals"]

    # ---- channels & watches ----------------------------------------------

    def channels(self) -> dict:
        """Typed channels and the evidence-tier ladder."""
        return self._http("GET", "/v1/channels")

    def register_channel(self, name: str, **spec) -> dict:
        """Self-register a typed channel (doc, default_half_life_s,
        decay_kernel, reinforce, cross_inhibits, ...)."""
        return self._http("POST", "/v1/channels", body={"name": name, **spec})

    def watch(self, *, name: str = "", resource_prefix: str = "",
              outcome_map: dict[str, str] | None = None,
              evidence_tier: str = "") -> "WatchHandle":
        """Register a derivation rule: push state transitions, get deposits.

        Returns a WatchHandle whose .ingest(resource, outcome=...) turns
        transitions into watch-derived signals without mark calls.
        """
        out = self._http("POST", "/v1/watches", body={
            "agent": self.id, "name": name, "resource_prefix": resource_prefix,
            "map": outcome_map or {}, "evidence_tier": evidence_tier})
        return WatchHandle(self, out["watch"]["id"], out["ingest_path"])

    def subscribe_channel(self, kind: str, callback, *, prefix: str = "") -> threading.Event:
        """React to live deposits on a channel instead of polling sniff.

        Spawns a daemon thread reading the substrate's SSE stream and calls
        callback(signal_dict) for each new signal of the given kind (and
        optional resource prefix). Returns a threading.Event; set() it to
        stop the subscription.
        """
        stop = threading.Event()

        def pump():
            req = urllib.request.Request(self.base + "/v1/events")
            try:
                with urllib.request.urlopen(req) as resp:
                    event_type = ""
                    for raw in resp:
                        if stop.is_set():
                            return
                        line = raw.decode("utf-8", "replace").rstrip("\n")
                        if line.startswith("event: "):
                            event_type = line[7:].strip()
                        elif line.startswith("data: ") and event_type == "signal":
                            try:
                                sig = json.loads(line[6:]).get("payload") or {}
                            except ValueError:
                                continue
                            if sig.get("kind") != kind:
                                continue
                            if prefix and not str(sig.get("resource", "")).startswith(prefix):
                                continue
                            callback(sig)
            except (urllib.error.URLError, OSError):
                return  # substrate went away; subscriber loops can re-subscribe

        threading.Thread(target=pump, daemon=True).start()
        return stop

    # ---- response thresholds ----------------------------------------------

    def responds_to(self, signal: dict) -> bool:
        """Probability-weighted response: should this agent act on a signal?

        Implements the response-threshold model: below the agent's threshold
        for the kind, never; above it, with probability rising with the
        signal's (effective) intensity. Agents with different thresholds
        divide labor without assignment.
        """
        threshold = self.response_thresholds.get(signal.get("kind", ""), 0)
        intensity = signal.get("effective_intensity") or signal.get("intensity", 0)
        if intensity < threshold:
            return False
        if threshold <= 0:
            return True
        # sigmoid-free classic form: p = I^2 / (I^2 + threshold^2)
        p = intensity * intensity / (intensity * intensity + threshold * threshold)
        return random.random() < p

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

    def _ingest(self, path: str, body: dict) -> dict:
        return self._http("POST", path, body=body)

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


class WatchHandle:
    """Handle on a registered watch: push transitions, deposits come out."""

    def __init__(self, agent: Agent, watch_id: str, ingest_path: str):
        self._agent = agent
        self.id = watch_id
        self.ingest_path = ingest_path

    def ingest(self, resource: str, *, outcome: str = "", kind: str = "",
               subtype: str = "", intensity: float = 0, note: str = "",
               meta: dict[str, str] | None = None) -> dict:
        """Push one state transition; returns the derived, deposited signal."""
        return self._agent._ingest(self.ingest_path, {
            "resource": resource, "outcome": outcome, "kind": kind,
            "subtype": subtype, "intensity": intensity, "note": note,
            "meta": meta or {}})
