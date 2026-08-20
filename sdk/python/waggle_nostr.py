"""waggle_nostr — carrying field signals onto the ecosystem's Nostr wire.

A Waggle field is per-substrate: agents sharing one `waggled` see each other's
traces, and nobody else does. That is right for coordination — the field is
meant to be local and to decay — but it means a `gold` one swarm paid to find
is invisible to every other swarm, and a `dead-end` gets re-walked by anyone on
a different substrate. The trace evaporates at the deployment boundary.

This publishes marks onto the shared wire so a finding outlives its field.

## Not every mark is an assertion

The distinction this module draws, and the reason it is not a blind mirror of
`mark()`:

  * `gold`, `dead-end`, `warn` assert something **about the resource** — that
    it holds something valuable, that a path fails, that care is needed. Those
    are checkable by anyone, so they travel as **Crucible claims**
    (`kind:47001`) and are subject to being proven wrong.
  * `explored`, `claim`, `handoff`, `help` are facts about **the marking agent**
    — where it has been, what it is holding, what it wants. They are true by
    construction and nobody can falsify them, so publishing them as claims
    would inject unfalsifiable noise into a belief space whose entire premise
    is falsifiability. They travel as engrams only.

`ASSERTIVE_KINDS` is that line, and `is_assertive` is where a caller checks it.

## Half-lives already line up

Waggle signals decay and so do Crucible claims, for the same reason: knowledge
nobody refreshes should stop counting. A mark's `half_life_s` therefore becomes
the claim's half-life directly rather than being replaced by a default — the
field's own judgement about how fast this kind of finding goes stale is better
than anything this module could invent.

## This module does not reimplement the wire contract

`minipae.py` is the Python implementation for this ecosystem — BIP-340, NIP-44,
canonical NIP-01 serialization, the engram `d`-tag HMAC, slug grammar, and the
`build_slug`/`sign_event` adapter kit. One per language, because the contract's
failure mode is silent divergence and each extra copy is another chance to
diverge in a way only a *different* implementation can detect.

Waggle's own clients are zero-dependency by design; this module is the one
place that is not, and it is optional — nothing else in the SDK imports it.

    export PYTHONPATH=/path/to/minipae:$PYTHONPATH
"""
from __future__ import annotations

import json
from typing import Any, Dict, Optional

_MINIPAE_HINT = (
    "minipae is required for the Nostr wire channel. It is the Python "
    "implementation of the ecosystem wire contract; this module deliberately "
    "does not reimplement it. Put it on PYTHONPATH: "
    "export PYTHONPATH=/path/to/minipae:$PYTHONPATH"
)

try:  # pragma: no cover - trivial import guard
    import minipae as _m
except ImportError:  # pragma: no cover
    _m = None


def _minipae():
    """Return minipae, or raise with an actionable message.

    Deferred rather than raised at import time so the rest of the SDK keeps its
    zero-dependency promise on a box with no Nostr channel configured.
    """
    if _m is None:
        raise RuntimeError(_MINIPAE_HINT)
    return _m


# Owned elsewhere -- minipae for 30174, crucible-core for 47001. Changing one
# here in isolation breaks interoperability silently.
KIND_AGENT_ENGRAM = 30174
KIND_CLAIM = 47001

#: Namespace segment, registered in minipae's NAMESPACES.md before first write.
NAMESPACE = "waggle"

#: Mark kinds that assert something about the resource, and can therefore be
#: proven wrong. Everything else is a fact about the marking agent.
ASSERTIVE_KINDS = frozenset({"gold", "dead-end", "warn"})

#: Fallback when a mark carries no half-life. Two hours matches Waggle's own
#: bounded-channel default rather than being invented here.
DEFAULT_HALF_LIFE_SECS = 2 * 3600


def is_assertive(kind: str) -> bool:
    """True when a mark of this kind asserts something falsifiable.

    An `explored` mark is true by construction -- the agent did visit -- so
    nobody can prove it wrong, and a belief space built on falsifiability
    should not be asked to hold it.
    """
    return str(kind).strip().lower() in ASSERTIVE_KINDS


def slug_signal(resource: str, kind: str) -> str:
    """Engram slug for a mark on one resource.

    Resource URIs carry schemes and path separators (`repo://src/auth.go`),
    none of which minipae's slug grammar allows, so both segments are
    normalised. The full URI travels in the content.
    """
    return _minipae().build_slug(NAMESPACE, "signal", str(resource), str(kind))


def signal_record(signal: Dict[str, Any]) -> Dict[str, Any]:
    """The wire body for a mark.

    Flat and self-describing so a Rust, Julia or TypeScript reader can
    interpret it without importing the SDK. Keeps `evidence_tier` and `cost`,
    which are what let a reader weigh the mark rather than merely see it.
    """
    meta = signal.get("meta") or {}
    if isinstance(meta, str):
        try:
            meta = json.loads(meta)
        except (json.JSONDecodeError, TypeError):
            meta = {"raw": meta}

    return {
        "resource": str(signal.get("resource", "")),
        "kind": str(signal.get("kind", "")),
        "intensity": float(signal.get("intensity", 0.0) or 0.0),
        "half_life_s": float(signal.get("half_life_s", 0.0) or 0.0),
        "decay": str(signal.get("decay", "") or ""),
        "subtype": str(signal.get("subtype", "") or ""),
        "evidence_tier": str(signal.get("evidence_tier", "") or ""),
        "note": str(signal.get("note", "") or ""),
        "agent": str(signal.get("agent", "") or ""),
        "cost": signal.get("cost") or {},
        "meta": meta,
    }


def half_life_for(signal: Dict[str, Any]) -> int:
    """The claim half-life for a mark, taken from the mark itself.

    The field already decided how fast this kind of finding goes stale; a
    default here would override a real judgement with a guess.
    """
    hl = float(signal.get("half_life_s", 0.0) or 0.0)
    return int(hl) if hl > 0 else DEFAULT_HALF_LIFE_SECS


def build_signal_engram(
    signal: Dict[str, Any],
    seckey: bytes,
    owner_pubkey: bytes,
) -> Dict[str, Any]:
    """Build a signed NIP-AE engram recording a mark.

    Content is NIP-44 encrypted and the slug HMAC'd into the `d` tag by
    ``minipae.build_event`` -- a relay operator learns a swarm marked something
    without learning which resource.
    """
    record = signal_record(signal)
    return _minipae().build_event(
        slug_signal(record["resource"] or "unknown", record["kind"] or "mark"),
        record,
        seckey,
        owner_pubkey,
    )


def build_signal_claim(
    signal: Dict[str, Any],
    falsifier: str,
    seckey: bytes,
) -> Dict[str, Any]:
    """Build a signed Crucible claim asserting a mark is right about a resource.

    Refuses a non-assertive kind rather than publishing it: an `explored` mark
    cannot be proven wrong, and a claim that cannot fail is noise in a substrate
    whose whole premise is that assertions must be falsifiable.

    Refuses a missing falsifier for the same reason Crucible does -- it rejects
    such a claim at parse time, so failing here gives a clear error instead of
    a silent bounce at the relay.
    """
    record = signal_record(signal)

    if not is_assertive(record["kind"]):
        raise ValueError(
            f"mark kind {record['kind']!r} asserts nothing about the resource, "
            "only about the marking agent, so it cannot be a Crucible claim. "
            f"Assertive kinds: {sorted(ASSERTIVE_KINDS)}"
        )
    if not falsifier:
        raise ValueError(
            "a Crucible claim requires a falsifier; Crucible rejects claims without one"
        )

    half_life = half_life_for(signal)
    content = json.dumps(
        {
            "statement": f"{record['resource']} is {record['kind']}",
            "falsifier": falsifier,
            "resource": record["resource"],
            "kind": record["kind"],
            "intensity": record["intensity"],
            "evidence_tier": record["evidence_tier"],
            "note": record["note"],
            "half_life_secs": half_life,
        },
        separators=(",", ":"),
        # The id is hashed over this content, and Python's default escaping
        # yields an id no other implementation agrees with.
        ensure_ascii=False,
    )

    m = _minipae()
    tags = [
        ["falsifier", falsifier],
        ["signal", m.normalize_slug_segment(record["kind"] or "mark")],
        ["half_life", str(half_life)],
    ]
    return m.sign_event(KIND_CLAIM, content, tags, seckey)


def build_signal_events(
    signal: Dict[str, Any],
    seckey: bytes,
    owner_pubkey: bytes,
    falsifier: Optional[str] = None,
) -> Dict[str, Dict[str, Any]]:
    """Build the signed events for a mark without contacting a relay.

    The claim is included only when the mark is assertive *and* a falsifier is
    supplied. A non-assertive mark with a falsifier is not an error here -- the
    caller may be publishing a mixed batch -- it simply yields the engram alone.
    """
    events = {"engram": build_signal_engram(signal, seckey, owner_pubkey)}
    if falsifier and is_assertive(signal.get("kind", "")):
        events["claim"] = build_signal_claim(signal, falsifier, seckey)
    return events


async def publish_signal(
    signal: Dict[str, Any],
    seckey: bytes,
    owner_pubkey: bytes,
    relay: str,
    falsifier: Optional[str] = None,
    authenticated: bool = True,
) -> Dict[str, Any]:
    """Publish a mark to ``relay`` as an engram, and as a claim when assertive.

    Async because ``minipae.publish`` is -- it speaks websockets.

    Returns what the relay actually said, per event, unmodified. A rejection
    stays a rejection: minipae's own history includes a bug where an
    ``auth-required`` refusal read as ``ok: True``, and a `dead-end` that
    silently failed to publish is worse than one never sent -- another swarm
    walks the path believing nobody warned them.

    ``authenticated`` uses NIP-42, which the production Buzz relay requires.
    """
    m = _minipae()
    events = build_signal_events(signal, seckey, owner_pubkey, falsifier)

    results: Dict[str, Any] = {}
    for name, ev in events.items():
        if authenticated:
            results[name] = await m.publish_authenticated(relay, ev, seckey)
        else:
            results[name] = await m.publish(relay, ev)
    return results
