"""digest — human-legible field summaries from the Waggle journal.

Connection Map v2 round 2, #5. Everything renders live in Axiom, but there is
no digest — no "here's what the swarm learned this week" a human (or Ọ̀ṣun's
memory) can read without opening the galaxy. This job queries a rolling window
of the journal per territory and writes a plain-language Markdown summary of
what went hot/cold, what got tabooed and why, and what stayed robust vs. went
fragile. It is the actual interface between the swarm's emergent behavior and
human oversight.

Two layers, cleanly separated:

  1. Data assembly (stdlib only): windowed recall, Ọ̀ṣun's consolidated
     patterns, Vantage's cross-ecosystem divergences → a structured brief.
     This is the load-bearing part and always runs.

  2. Prose generation (optional): if the Anthropic SDK and a key are present,
     Claude turns the brief into readable narrative. Otherwise a deterministic
     Markdown template renders the same facts. The digest is useful with or
     without an LLM — the LLM only changes how the facts read, not what they are.

    python3 oracle/digest/digest.py --territory loom:// --hours 168 -o weekly.md

Requires waggled running with -data (recall needs a journal).
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from collections import defaultdict
from datetime import datetime, timedelta, timezone

WAGGLE = os.environ.get("WAGGLE_URL", "http://127.0.0.1:7777").rstrip("/")


def _get(path: str):
    try:
        with urllib.request.urlopen(WAGGLE + path, timeout=15) as resp:
            return json.loads(resp.read().decode())
    except (urllib.error.URLError, urllib.error.HTTPError, OSError, ValueError):
        return None


# ── layer 1: assemble the structured brief (stdlib only) ────────────────────

def assemble_brief(territory: str, hours: float) -> dict:
    """Pull the window's history and derive what changed. Returns a structured
    brief the prose layer (LLM or template) renders."""
    until = datetime.now(timezone.utc)
    since = until - timedelta(hours=hours)
    q = urllib.parse.urlencode({
        "territory": territory,
        "since": since.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "until": until.strftime("%Y-%m-%dT%H:%M:%SZ"),
    })
    out = _get(f"/v1/recall/window?{q}")
    events = (out or {}).get("events", [])

    # trajectory per resource: first vs last intensity, by kind
    first_seen: dict[tuple[str, str], float] = {}
    last_seen: dict[tuple[str, str], float] = {}
    counts: dict[str, int] = defaultdict(int)
    taboos: list[dict] = []
    bounded_track: dict[str, list[float]] = defaultdict(list)
    for ev in events:
        sig = ev["signal"]
        key = (sig["resource"], sig["kind"])
        inten = float(sig.get("intensity", 0))
        first_seen.setdefault(key, inten)
        last_seen[key] = inten
        counts[sig["kind"]] += 1
        if sig["kind"] == "taboo":
            taboos.append({
                "resource": sig["resource"],
                "justification": (sig.get("meta") or {}).get("justification", ""),
                "principle": (sig.get("meta") or {}).get("principle", ""),
                "at": ev["at"],
            })
        if sig["kind"] == "bounded":
            bounded_track[sig["resource"]].append(inten / 10.0)

    # what warmed / cooled (gold + explored trajectories)
    warmed, cooled = [], []
    for (res, kind), first in first_seen.items():
        if kind not in ("gold", "explored"):
            continue
        delta = last_seen[(res, kind)] - first
        if delta >= 1.0:
            warmed.append({"resource": res, "kind": kind, "delta": round(delta, 2)})
        elif delta <= -1.0:
            cooled.append({"resource": res, "kind": kind, "delta": round(delta, 2)})
    warmed.sort(key=lambda x: -x["delta"])
    cooled.sort(key=lambda x: x["delta"])

    # robustness: which resources stayed robust vs went fragile
    robust, fragile = [], []
    for res, track in bounded_track.items():
        if not track:
            continue
        start, end = track[0], track[-1]
        entry = {"resource": res, "start": round(start, 2), "end": round(end, 2)}
        if end >= 0.5 and start >= 0.5:
            robust.append(entry)
        elif end < 0.5 and start >= 0.5:
            fragile.append(entry)  # was robust, now brittle — the notable case
    fragile.sort(key=lambda x: x["end"])

    return {
        "territory": territory or "(whole field)",
        "hours": hours,
        "window": {"since": since.isoformat(), "until": until.isoformat()},
        "event_count": len(events),
        "kind_counts": dict(counts),
        "warmed": warmed[:10],
        "cooled": cooled[:10],
        "taboos": taboos[:10],
        "robust": robust[:10],
        "fragile": fragile[:10],
        "osun_patterns": _osun_consolidation(territory),
        "federation_divergences": _federation_divergences(),
    }


def _osun_consolidation(territory: str) -> dict:
    """Ọ̀ṣun's consolidated resonance patterns for this territory (§6.4) — the
    'what mattered' filter, if she has run. The digest is the 'explain it to a
    human' layer on top."""
    key = "".join(c if c.isalnum() else "-" for c in territory).strip("-") or "field"
    out = _get(f"/v1/memory/osun/consolidated/{key}")
    return (out or {}).get("value", {}) if out else {}


def _federation_divergences() -> list[dict]:
    """Cross-ecosystem robustness divergences Vantage surfaced (§4.5) — worth a
    digest section: 'your bounded verdicts diverged from partner X this week'."""
    out = _get("/v1/sniff?kind=warn&limit=50")
    divs = []
    for sig in (out or {}).get("signals", []):
        meta = sig.get("meta") or {}
        if "remote_field" in meta and "remote_stability" in meta:
            divs.append({
                "resource": sig["resource"],
                "local_stability": meta.get("local_stability"),
                "remote_stability": meta.get("remote_stability"),
                "remote_field": meta.get("remote_field"),
            })
    return divs[:10]


# ── layer 2a: deterministic Markdown template (always available) ────────────

def render_template(brief: dict) -> str:
    b = brief
    lines = [
        f"# Field digest — {b['territory']}",
        "",
        f"*Window: last {b['hours']:.0f}h ({b['event_count']} deposits). "
        f"Generated {datetime.now(timezone.utc):%Y-%m-%d %H:%M UTC}.*",
        "",
        "## Activity",
        "",
        ", ".join(f"{k}: {v}" for k, v in sorted(b["kind_counts"].items(), key=lambda x: -x[1]))
        or "no activity in window",
        "",
    ]

    def section(title, items, fmt):
        lines.append(f"## {title}")
        lines.append("")
        if not items:
            lines.append("_none_")
        else:
            lines.extend(fmt(i) for i in items)
        lines.append("")

    section("Warmed up", b["warmed"], lambda i: f"- `{i['resource']}` ({i['kind']}) +{i['delta']}")
    section("Cooled off", b["cooled"], lambda i: f"- `{i['resource']}` ({i['kind']}) {i['delta']}")
    section("Went fragile (was robust, now brittle)", b["fragile"],
            lambda i: f"- `{i['resource']}` stability {i['start']} → {i['end']}")
    section("Stayed robust", b["robust"],
            lambda i: f"- `{i['resource']}` stability {i['start']} → {i['end']}")
    section("Tabooed (ethically excluded)", b["taboos"],
            lambda i: f"- `{i['resource']}` — {i['principle'] or 'excluded'}: {i['justification'] or 'no trace'}")
    section("Cross-ecosystem divergences", b["federation_divergences"],
            lambda i: f"- `{i['resource']}`: local {i['local_stability']} vs {i['remote_field']} {i['remote_stability']}")

    patterns = (b["osun_patterns"] or {}).get("patterns")
    if patterns:
        lines.append("## Ọ̀ṣun's retained wisdom")
        lines.append("")
        for res, e in list(patterns.items())[:10]:
            lines.append(f"- `{res}` — resonance {e.get('resonance')}, kinds {e.get('kinds')}")
        lines.append("")

    return "\n".join(lines)


# ── layer 2b: optional Claude-generated prose ───────────────────────────────

def render_with_claude(brief: dict) -> str | None:
    """If the Anthropic SDK and a key are available, have Claude write the
    narrative. Returns None to fall back to the template."""
    try:
        import anthropic
    except ImportError:
        return None
    if not (os.environ.get("ANTHROPIC_API_KEY") or os.environ.get("ANTHROPIC_AUTH_TOKEN")):
        # a profile may still exist, but don't block the digest on it
        try:
            client = anthropic.Anthropic()
            client.models.list(limit=1)  # cheap probe that credentials resolve
        except Exception:
            return None
    else:
        client = anthropic.Anthropic()

    prompt = (
        "You are writing a weekly digest of an agent swarm's stigmergic field for a "
        "human overseer who did not watch the live visualization. Turn the structured "
        "brief below into a clear, plain-language Markdown summary: what the swarm "
        "focused on, what territories warmed or cooled, what was ethically excluded "
        "(taboo) and why, and — most important — what stayed robust vs. what went "
        "fragile (bounded stability falling below 0.5 means previously-robust logic is "
        "now brittle; call these out as risks). Lead with the single most important "
        "thing an overseer should know. Be concise and specific; use the real resource "
        "URIs. Do not invent facts not in the brief.\n\n"
        f"Brief (JSON):\n{json.dumps(brief, indent=2)}"
    )
    try:
        msg = client.messages.create(
            model="claude-opus-4-8",
            max_tokens=4096,
            thinking={"type": "adaptive"},
            output_config={"effort": "medium"},
            messages=[{"role": "user", "content": prompt}],
        )
    except Exception:
        return None
    if getattr(msg, "stop_reason", None) == "refusal":
        return None
    text = "".join(b.text for b in msg.content if getattr(b, "type", "") == "text")
    return text or None


def main(argv):
    ap = argparse.ArgumentParser(description="Human-legible Waggle field digest")
    ap.add_argument("--territory", default="", help="URI prefix to summarize (default: whole field)")
    ap.add_argument("--hours", type=float, default=168, help="rolling window in hours (default 168 = 1 week)")
    ap.add_argument("-o", "--output", default="", help="write to file (default: stdout)")
    ap.add_argument("--no-llm", action="store_true", help="skip Claude, always use the deterministic template")
    args = ap.parse_args(argv[1:])

    brief = assemble_brief(args.territory, args.hours)
    md = None if args.no_llm else render_with_claude(brief)
    source = "claude-opus-4-8"
    if md is None:
        md = render_template(brief)
        source = "template"
    md += f"\n\n---\n*Digest source: {source}. Data: Waggle recall window.*\n"

    if args.output:
        with open(args.output, "w") as f:
            f.write(md)
        print(f"wrote {args.output} ({source}, {brief['event_count']} events)", file=sys.stderr)
    else:
        print(md)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
