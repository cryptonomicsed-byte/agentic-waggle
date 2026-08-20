"""Tests for the Waggle Nostr wire channel (stdlib unittest).

    PYTHONPATH=/path/to/minipae python3 -m unittest test_waggle_nostr

Skipped rather than failed when minipae is absent, so the SDK's zero-dependency
promise holds on a box with no Nostr channel configured.
"""
from __future__ import annotations

import json
import unittest

import waggle_nostr as wn

try:
    import minipae as m
    HAVE_MINIPAE = True
except ImportError:  # pragma: no cover
    m = None
    HAVE_MINIPAE = False


A_GOLD = {
    "agent": "scout-1",
    "resource": "repo://src/auth.go",
    "kind": "gold",
    "intensity": 5.0,
    "half_life_s": 1800.0,
    "decay": "power",
    "evidence_tier": "watch-derived",
    "note": "vuln at line 42",
    "cost": {"tokens": 900},
    "meta": {"run": "7"},
}

AN_EXPLORED = {**A_GOLD, "kind": "explored", "note": "walked it"}

SECKEY = bytes([0x44]) * 32


def _pubkey():
    return m.pubkey_from_secret(int.from_bytes(SECKEY, "big"))


class TestAssertiveness(unittest.TestCase):
    """The line between an assertion and a self-report."""

    def test_findings_about_a_resource_are_assertive(self):
        for kind in ("gold", "dead-end", "warn"):
            self.assertTrue(wn.is_assertive(kind), kind)

    def test_facts_about_the_marking_agent_are_not(self):
        # These are true by construction: the agent did explore, is holding a
        # claim, did hand off. Nobody can prove them wrong, so they must not
        # enter a belief space built on falsifiability.
        for kind in ("explored", "claim", "handoff", "help"):
            self.assertFalse(wn.is_assertive(kind), kind)

    def test_matching_ignores_case_and_padding(self):
        self.assertTrue(wn.is_assertive("  GOLD "))


class TestHalfLife(unittest.TestCase):
    def test_takes_the_marks_own_half_life(self):
        # The field already judged how fast this finding goes stale; a default
        # would override a real decision with a guess.
        self.assertEqual(wn.half_life_for(A_GOLD), 1800)

    def test_falls_back_only_when_the_mark_has_none(self):
        self.assertEqual(wn.half_life_for({**A_GOLD, "half_life_s": 0}),
                         wn.DEFAULT_HALF_LIFE_SECS)


class TestSignalRecord(unittest.TestCase):
    def test_keeps_the_fields_that_let_a_reader_weigh_the_mark(self):
        rec = wn.signal_record(A_GOLD)
        self.assertEqual(rec["evidence_tier"], "watch-derived")
        self.assertEqual(rec["cost"], {"tokens": 900})
        self.assertEqual(rec["intensity"], 5.0)

    def test_meta_stored_as_text_is_parsed_back_to_structure(self):
        rec = wn.signal_record({**A_GOLD, "meta": json.dumps({"a": 1})})
        self.assertEqual(rec["meta"], {"a": 1})

    def test_unparseable_meta_is_preserved_rather_than_dropped(self):
        self.assertEqual(wn.signal_record({**A_GOLD, "meta": "nope"})["meta"],
                         {"raw": "nope"})


@unittest.skipUnless(HAVE_MINIPAE, "minipae not on PYTHONPATH")
class TestSlugs(unittest.TestCase):
    def test_a_resource_uri_still_yields_a_valid_slug(self):
        # repo://src/auth.go carries a scheme and separators, none of which
        # minipae's grammar allows.
        slug = wn.slug_signal("repo://src/auth.go", "gold")
        self.assertTrue(m.validate_slug(slug), slug)
        self.assertEqual(slug, "mem/waggle/signal/repo-src-auth-go/gold")

    def test_different_kinds_on_one_resource_do_not_collide(self):
        # Engrams are addressable: a resource-only slug would let a later warn
        # silently replace an earlier gold.
        self.assertNotEqual(
            wn.slug_signal("repo://x", "gold"),
            wn.slug_signal("repo://x", "warn"),
        )


@unittest.skipUnless(HAVE_MINIPAE, "minipae not on PYTHONPATH")
class TestEvents(unittest.TestCase):
    def test_engram_is_a_valid_signed_nip_ae_event(self):
        ev = wn.build_signal_engram(A_GOLD, SECKEY, _pubkey())
        self.assertEqual(ev["kind"], wn.KIND_AGENT_ENGRAM)
        self.assertEqual(ev["id"], m.event_id(ev))
        self.assertTrue(
            m.schnorr_verify(bytes.fromhex(ev["id"]), _pubkey(),
                             bytes.fromhex(ev["sig"]))
        )

    def test_engram_content_decrypts_back_to_the_mark(self):
        pub = _pubkey()
        ev = wn.build_signal_engram(A_GOLD, SECKEY, pub)
        recovered = json.loads(m.nip44_decrypt(ev["content"],
                                               m.conversation_key(SECKEY, pub)))
        self.assertEqual(recovered["resource"], "repo://src/auth.go")
        self.assertEqual(recovered["kind"], "gold")

    def test_the_marked_resource_never_appears_in_the_clear(self):
        # A relay operator should not be able to enumerate which files a swarm
        # is finding vulnerabilities in.
        ev = wn.build_signal_engram(A_GOLD, SECKEY, _pubkey())
        self.assertNotIn("auth.go", json.dumps(ev))

    def test_claim_carries_the_marks_half_life(self):
        ev = wn.build_signal_claim(A_GOLD, "sha256:abc", SECKEY)
        self.assertEqual(ev["kind"], wn.KIND_CLAIM)
        self.assertEqual(json.loads(ev["content"])["half_life_secs"], 1800)
        self.assertTrue(
            m.schnorr_verify(bytes.fromhex(ev["id"]), _pubkey(),
                             bytes.fromhex(ev["sig"]))
        )

    def test_a_non_assertive_mark_cannot_become_a_claim(self):
        # The load-bearing rule: an `explored` mark is unfalsifiable, and a
        # claim that cannot fail is noise in Crucible.
        with self.assertRaises(ValueError) as ctx:
            wn.build_signal_claim(AN_EXPLORED, "sha256:abc", SECKEY)
        self.assertIn("asserts nothing about the resource", str(ctx.exception))

    def test_a_claim_without_a_falsifier_is_refused(self):
        with self.assertRaises(ValueError):
            wn.build_signal_claim(A_GOLD, "", SECKEY)

    def test_build_events_emits_a_claim_only_for_assertive_marks(self):
        pub = _pubkey()
        self.assertEqual(
            set(wn.build_signal_events(A_GOLD, SECKEY, pub, falsifier="sha256:abc")),
            {"engram", "claim"},
        )
        # A non-assertive mark in a mixed batch yields the engram alone rather
        # than failing the whole batch.
        self.assertEqual(
            set(wn.build_signal_events(AN_EXPLORED, SECKEY, pub, falsifier="sha256:abc")),
            {"engram"},
        )
        self.assertEqual(set(wn.build_signal_events(A_GOLD, SECKEY, pub)), {"engram"})


if __name__ == "__main__":
    unittest.main()
