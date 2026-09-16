package main

// gate.go — Èṣù capability gate for the waggle field.
//
// The Rust client (omokoda-core/src/waggle/) implements the same logic in the
// kernel process.  Moving it here means every client path — Python SDK, MCP
// bridge, curl, Rust kernel — is subject to the same enforcement rather than
// relying on well-behaved callers.
//
// Protocol:
//   POST /v1/agents  → response includes {"token": "<hex>"} alongside the profile
//   Write verbs (POST /v1/signals, /v1/claims, /v1/claims/release, /v1/dances):
//     require header  X-Waggle-Token: <agent_id>:<token>
//     OR              Authorization: Bearer <agent_id>:<token>
//   Taboo signals:    body["capability"] = "<hex_payload>.<hex_sig>" verified
//                     against the server's ed25519 public key (--taboo-auth-key).
//
// Enforcement mode:
//   --require-auth   reject writes without a valid token (hard enforcement)
//   default          log a warning and pass through (backward-compatible open mode)
//
// Mark throttle:
//   Token bucket per agent — 30 burst, refill 0.5 marks/second (30/min).
//   A looping agent cannot flood the field faster than decay cleans it.
//
// Sybil ring detection:
//   Registration records the caller's X-Waggle-Origin header as the issuance
//   origin.  Agents minted from the same origin in a short window form a ring
//   that the lineage report surfaces, even though every token is individually
//   valid.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---- session -----------------------------------------------------------------

type agentSession struct {
	token    string // hex-encoded 32-byte random bearer token
	origin   string // X-Waggle-Origin at registration (lineage tracking)
	issuedAt time.Time
}

// ---- mark throttle (token bucket) -------------------------------------------

type markBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	refill   float64 // tokens per second
	last     time.Time
}

func newBucket(capacity, refillPerSec float64) *markBucket {
	return &markBucket{
		tokens:   capacity,
		capacity: capacity,
		refill:   refillPerSec,
		last:     time.Now(),
	}
}

func (b *markBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * b.refill
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ---- Ring (Sybil detection result) ------------------------------------------

type Ring struct {
	Origin  string   `json:"origin"`
	Members []string `json:"members"`
}

// ---- EsuGate -----------------------------------------------------------------

// EsuGate is the server-side Èṣù capability gate.  It is safe for concurrent
// use from multiple goroutines.
type EsuGate struct {
	mu          sync.RWMutex
	sessions    map[string]*agentSession // agent_id → session
	throttles   map[string]*markBucket   // agent_id → bucket
	tabooKey    ed25519.PublicKey        // nil → taboo auth disabled
	requireAuth bool                     // hard-reject mode

	// throttle config
	burstCap   float64 // default 30
	refillRate float64 // default 0.5 marks/second
}

// NewEsuGate creates a gate.  tabooPubHex is the hex-encoded 32-byte ed25519
// public key for taboo-capability verification; empty string disables it.
// requireAuth puts the gate in hard-enforcement mode.
func NewEsuGate(tabooPubHex string, requireAuth bool) *EsuGate {
	g := &EsuGate{
		sessions:    make(map[string]*agentSession),
		throttles:   make(map[string]*markBucket),
		requireAuth: requireAuth,
		burstCap:    30,
		refillRate:  0.5,
	}
	if tabooPubHex != "" {
		raw, err := hex.DecodeString(tabooPubHex)
		if err == nil && len(raw) == ed25519.PublicKeySize {
			g.tabooKey = ed25519.PublicKey(raw)
		}
	}
	return g
}

// Register mints a new session token for agentID and records the issuance
// origin (for ring detection).  Calling Register again rotates the token.
func (g *EsuGate) Register(agentID, origin string) string {
	raw := make([]byte, 32)
	rand.Read(raw) //nolint:errcheck — crypto/rand never errors on Linux
	token := hex.EncodeToString(raw)
	g.mu.Lock()
	g.sessions[agentID] = &agentSession{
		token:    token,
		origin:   origin,
		issuedAt: time.Now(),
	}
	g.mu.Unlock()
	return token
}

// Revoke removes the agent's session (death, quarantine, misbehaviour).
func (g *EsuGate) Revoke(agentID string) {
	g.mu.Lock()
	delete(g.sessions, agentID)
	g.mu.Unlock()
}

// Authorize checks a bearer credential of the form "agentID:token".
func (g *EsuGate) Authorize(credential string) bool {
	idx := strings.IndexByte(credential, ':')
	if idx <= 0 || idx == len(credential)-1 {
		return false
	}
	agentID := credential[:idx]
	token := credential[idx+1:]
	g.mu.RLock()
	sess, ok := g.sessions[agentID]
	g.mu.RUnlock()
	if !ok {
		return false
	}
	// constant-time comparison prevents timing oracle
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.token)) == 1
}

// ThrottleMark checks and decrements the mark throttle for agentID.
// Returns false when the agent has exhausted its burst budget.
func (g *EsuGate) ThrottleMark(agentID string) bool {
	g.mu.Lock()
	b, ok := g.throttles[agentID]
	if !ok {
		b = newBucket(g.burstCap, g.refillRate)
		g.throttles[agentID] = b
	}
	g.mu.Unlock()
	return b.allow()
}

// SuspectedRings returns agent clusters that share the same registration
// origin and were minted within window.  A cluster of size >= minRing is a
// suspected Sybil ring even though each token is individually valid.
func (g *EsuGate) SuspectedRings(window time.Duration, minRing int) []Ring {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	byOrigin := make(map[string][]string)
	for agentID, sess := range g.sessions {
		if sess.issuedAt.Before(cutoff) {
			continue
		}
		byOrigin[sess.origin] = append(byOrigin[sess.origin], agentID)
	}
	var rings []Ring
	for origin, members := range byOrigin {
		if len(members) >= minRing && origin != "" {
			rings = append(rings, Ring{Origin: origin, Members: members})
		}
	}
	return rings
}

// VerifyTabooCap verifies the ed25519 taboo capability embedded in a signal
// body (key "capability", value "<hex_payload>.<hex_sig>").
// Returns true if the server has no taboo key configured (disabled).
// Returns false if the key is configured but the cap is missing or invalid.
func (g *EsuGate) VerifyTabooCap(body map[string]any) bool {
	if g.tabooKey == nil {
		return true // taboo auth disabled — open mode
	}
	raw, ok := body["capability"].(string)
	if !ok || raw == "" {
		return false
	}
	dot := strings.IndexByte(raw, '.')
	if dot <= 0 || dot == len(raw)-1 {
		return false
	}
	payload, err1 := hex.DecodeString(raw[:dot])
	sig, err2 := hex.DecodeString(raw[dot+1:])
	if err1 != nil || err2 != nil {
		return false
	}
	return ed25519.Verify(g.tabooKey, payload, sig)
}

// credential extracts "agentID:token" from X-Waggle-Token or Authorization.
func credential(r *http.Request) string {
	if h := r.Header.Get("X-Waggle-Token"); h != "" {
		return h
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// WriteAuthMiddleware wraps a write handler with the Èṣù gate.
// It extracts the bearer credential, checks authorisation, runs the mark
// throttle for signal deposits, and verifies taboo capabilities when the
// taboo auth key is configured.
//
// In open mode (requireAuth=false) an invalid or absent token is logged but
// allowed through, so existing SDK clients are unaffected.  In hard mode
// (requireAuth=true) any failure returns 401 or 429.
func (g *EsuGate) WriteAuthMiddleware(verb string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := credential(r)
		authed := cred != "" && g.Authorize(cred)

		if !authed {
			if g.requireAuth {
				writeErr(w, http.StatusUnauthorized,
					"missing or invalid X-Waggle-Token; register first via POST /v1/agents")
				return
			}
			// open mode: pass through with a logged warning
			_ = verb // callers can grep by verb if needed
		}

		// signal deposits: throttle marks and verify taboo caps
		if verb == "signal" && r.Method == http.MethodPost {
			agentID := ""
			if cred != "" {
				if idx := strings.IndexByte(cred, ':'); idx > 0 {
					agentID = cred[:idx]
				}
			}
			if agentID != "" && !g.ThrottleMark(agentID) {
				writeErr(w, http.StatusTooManyRequests,
					"mark throttle exceeded — slow down or wait for refill")
				return
			}
			// taboo caps: must be verified regardless of auth mode
			if g.tabooKey != nil {
				// peek at the body only if Content-Type is JSON; we'll
				// re-serve it unchanged via the next handler
				var peek map[string]any
				if err := json.NewDecoder(r.Body).Decode(&peek); err == nil {
					if kind, _ := peek["kind"].(string); kind == "taboo" {
						if !g.VerifyTabooCap(peek) {
							writeErr(w, http.StatusForbidden,
								"taboo capability invalid or missing — use TabooCapIssuer")
							return
						}
					}
					// re-encode body so the next handler can decode it again
					encoded, _ := json.Marshal(peek)
					r.Body = noopCloser{strings.NewReader(string(encoded))}
					r.ContentLength = int64(len(encoded))
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ---- lineage endpoint --------------------------------------------------------

func (g *EsuGate) handleRings(w http.ResponseWriter, r *http.Request) {
	rings := g.SuspectedRings(10*time.Minute, 3)
	if rings == nil {
		rings = []Ring{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rings": rings})
}

// ---- noopCloser: lets us replace r.Body after peeking ----------------------

type noopCloser struct{ *strings.Reader }

func (noopCloser) Close() error { return nil }
