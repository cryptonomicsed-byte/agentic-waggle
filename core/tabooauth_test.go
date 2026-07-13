package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// A fixed seed, payload and clock so the signature is deterministic (Ed25519 is
// RFC 8032 deterministic). The Rust issuer test (Omo-Koda2 waggle::taboo_cap)
// pins the SAME vector, proving the two implementations interoperate without a
// live handshake: same key + same message ⇒ identical signature ⇒ each verifies
// the other's tokens.
var vectorSeed = mustHex("0101010101010101010101010101010101010101010101010101010101010101")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func makeToken(t *testing.T, priv ed25519.PrivateKey, p tabooCapPayload) string {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, payload)
	return hex.EncodeToString(payload) + "." + hex.EncodeToString(sig)
}

func TestTabooAuthVectorAndPaths(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(vectorSeed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	t.Logf("VECTOR pubkey = %s", pubHex)

	ta, err := NewTabooAuth(pubHex, true)
	if err != nil {
		t.Fatal(err)
	}

	// the canonical cross-language vector: fixed everything ⇒ fixed token
	vec := tabooCapPayload{
		Agent: "obatala-child-1", Scope: "taboo", Lineage: "obatala:verified",
		IssuedAt: 1000000000, Expires: 2000000000,
	}
	token := makeToken(t, priv, vec)
	t.Logf("VECTOR token  = %s", token)

	clock := time.Unix(1500000000, 0) // between iat and exp
	if !ta.verify("obatala-child-1", token, clock) {
		t.Fatal("valid capability rejected")
	}

	// wrong bearer, wrong scope, expired, tampered signature, malformed
	if ta.verify("someone-else", token, clock) {
		t.Error("accepted token for a different agent")
	}
	badScope := makeToken(t, priv, tabooCapPayload{
		Agent: "a", Scope: "gold", IssuedAt: 1, Expires: 2000000000})
	if ta.verify("a", badScope, clock) {
		t.Error("accepted non-taboo scope")
	}
	if ta.verify("obatala-child-1", token, time.Unix(2000000001, 0)) {
		t.Error("accepted expired token")
	}
	tampered := token[:len(token)-2] + "00"
	if ta.verify("obatala-child-1", tampered, clock) {
		t.Error("accepted tampered signature")
	}
	for _, junk := range []string{"", ".", "zz.zz", "abcd", hex.EncodeToString([]byte("x")) + "."} {
		if ta.verify("a", junk, clock) {
			t.Errorf("accepted malformed token %q", junk)
		}
	}

	// a different key must not verify this token
	other := ed25519.NewKeyFromSeed(mustHex("0202020202020202020202020202020202020202020202020202020202020202"))
	otherTA, _ := NewTabooAuth(hex.EncodeToString(other.Public().(ed25519.PublicKey)), true)
	if otherTA.verify("obatala-child-1", token, clock) {
		t.Error("token verified under the wrong public key")
	}
}
