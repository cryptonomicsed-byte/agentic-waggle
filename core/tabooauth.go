package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// taboo authentication — the substrate side of Èṣù's capability gate.
//
// The bare field treats every channel alike: a scent mark is a scent mark.
// That is right for gold/explored/bounded, which only steer *search* — a bad
// one costs a wasted look. taboo is different: it censors a path, so a spammed
// taboo is a denial-of-service on a legitimate route (the ecosystem benchmark
// showed a lone griefer hiding in concurrent load, invisible to rate-based
// anomaly detection). The fix is not more detection; it is authentication —
// only an agent bearing a capability minted by Èṣù for verified Ọbàtálá-lineage
// identities may leave a taboo.
//
// Core stays stdlib-only: it does not *issue* capabilities (that is Èṣù, in
// Omo-Koda2, which holds the signing key and enforces the lineage bar). Core
// only *verifies* them, with crypto/ed25519. The daemon is configured with
// Èṣù's public key; a taboo deposit carries a token; core checks the signature,
// scope, bearer and expiry. Deterministic Ed25519 (RFC 8032) means the Rust
// issuer and this Go verifier agree byte-for-byte without any shared runtime.
//
// The token is `hex(payload) "." hex(signature)`, where payload is the compact
// JSON below signed as raw bytes. hex (not base64) on both ends keeps the wire
// format trivially identical across languages.

// tabooCapPayload is the signed grant. The exact payload bytes travel inside
// the token, so there is no canonicalization problem: core verifies the
// signature over the bytes it received, then parses them.
type tabooCapPayload struct {
	Agent    string `json:"agent"`   // the only agent that may bear this token
	Scope    string `json:"scope"`   // must be "taboo"
	Lineage  string `json:"lineage"` // Ọbàtálá-lineage proof the issuer checked
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

// TabooAuth verifies taboo capabilities against Èṣù's public key. When enforce
// is set, an unauthenticated taboo deposit is refused; otherwise (a transition
// period) it is accepted but marked taboo_authenticated=false so the exposure
// is visible rather than silent.
type TabooAuth struct {
	pub     ed25519.PublicKey
	enforce bool
}

// NewTabooAuth builds a verifier from Èṣù's hex-encoded ed25519 public key.
func NewTabooAuth(pubHex string, enforce bool) (*TabooAuth, error) {
	b, err := hex.DecodeString(strings.TrimSpace(pubHex))
	if err != nil {
		return nil, errors.New("taboo-auth key must be hex: " + err.Error())
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, errors.New("taboo-auth key must be a 32-byte ed25519 public key")
	}
	return &TabooAuth{pub: ed25519.PublicKey(b), enforce: enforce}, nil
}

// verify reports whether token is a valid taboo capability for agent at now.
// It never errors: any malformation, bad signature, wrong scope, wrong bearer,
// or expiry is simply "not authenticated".
func (t *TabooAuth) verify(agent, token string, now time.Time) bool {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return false
	}
	payload, err := hex.DecodeString(token[:dot])
	if err != nil {
		return false
	}
	sig, err := hex.DecodeString(token[dot+1:])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	if !ed25519.Verify(t.pub, payload, sig) {
		return false
	}
	var p tabooCapPayload
	if json.Unmarshal(payload, &p) != nil {
		return false
	}
	if p.Scope != "taboo" || p.Agent != agent {
		return false
	}
	return now.Unix() < p.Expires
}
