package main

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Signal is a single scent mark deposited on a resource by an agent.
// Its intensity decays exponentially with the configured half-life, so the
// field self-cleans: stale knowledge fades instead of accumulating as noise.
type Signal struct {
	ID          string            `json:"id"`
	Agent       string            `json:"agent"`
	Resource    string            `json:"resource"`
	Kind        string            `json:"kind"`
	Intensity   float64           `json:"intensity"`
	HalfLifeS   float64           `json:"half_life_s"`
	Decay       string            `json:"decay,omitempty"` // "" or "exp" (exponential), "power" (heavy tail)
	Alpha       float64           `json:"alpha,omitempty"` // power-law exponent, default 1
	Note        string            `json:"note,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
	DepositedAt time.Time         `json:"deposited_at"`
}

// At returns the decayed intensity of the signal at time t.
//
// The default kernel is exponential: intensity * 2^(-age/half_life). The
// "power" kernel is heavy-tailed: intensity * (1 + age/scale)^-alpha, with
// scale calibrated so the signal still halves at exactly one half-life. Both
// kernels agree at age 0 and age half_life; past that the power law decays
// far more slowly — after 10 half-lives an exponential gold signal is at
// 0.1% while a power-law one (alpha=1) is still at ~9%. Use it for findings
// that should fade to background, not to nothing.
func (s *Signal) At(t time.Time) float64 {
	age := t.Sub(s.DepositedAt).Seconds()
	if age <= 0 {
		return s.Intensity
	}
	if s.Decay == "power" {
		alpha := s.Alpha
		if alpha <= 0 {
			alpha = 1
		}
		scale := s.HalfLifeS / (math.Pow(2, 1/alpha) - 1)
		return s.Intensity * math.Pow(1+age/scale, -alpha)
	}
	return s.Intensity * math.Exp2(-age/s.HalfLifeS)
}

// snapshot returns a copy of the signal with Intensity/DepositedAt projected
// to time t, for serving over the wire.
func (s *Signal) snapshot(t time.Time) Signal {
	c := *s
	c.Intensity = s.At(t)
	return c
}

const (
	// signals below this intensity are considered evaporated
	evaporated = 0.01
	// reinforcement can never push a signal above this ceiling
	maxIntensity = 10.0
	// defaults applied when a deposit omits them
	DefaultIntensity = 1.0
	DefaultHalfLifeS = 1800 // 30 minutes
)

// Well-known signal kinds. The field accepts any kind string — these are the
// vocabulary reference agents share. See docs/PROTOCOL.md.
var WellKnownKinds = []string{
	"explored",  // an agent has looked at this resource
	"claimed",   // informational echo of a lease
	"gold",      // high-value finding here; others should look
	"dead-end",  // this path was tried and failed; avoid repeating it
	"help",      // an agent is stuck here and requests assistance
	"warn",      // hazard: destructive, flaky, or costly territory
	"handoff",   // context parked here for a successor agent
	"heartbeat", // liveness trace of a long-running agent
}

// Field is the shared stigmergic medium: a concurrent map of resource URI ->
// deposited signals. Decay is computed lazily on read; a periodic sweep
// removes evaporated signals.
type Field struct {
	mu      sync.RWMutex
	byRes   map[string][]*Signal
	clock   func() time.Time
	onEvent func(kind string, payload any) // optional event sink (SSE hub / store)
}

func NewField() *Field {
	return &Field{byRes: make(map[string][]*Signal), clock: time.Now}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Deposit adds a signal to the field. A deposit by the same agent, on the
// same resource, with the same kind reinforces the existing signal: the new
// intensity is the current decayed value plus the deposit, capped at
// maxIntensity, with the decay clock reset. This is pheromone reinforcement —
// repeated marking keeps a trail hot, while abandoned trails evaporate.
func (f *Field) Deposit(sig Signal) Signal {
	now := f.clock()
	if sig.Intensity <= 0 {
		sig.Intensity = DefaultIntensity
	}
	if sig.Intensity > maxIntensity {
		sig.Intensity = maxIntensity
	}
	if sig.HalfLifeS <= 0 {
		sig.HalfLifeS = DefaultHalfLifeS
	}
	if sig.Decay != "power" {
		sig.Decay = "" // canonical exponential
		sig.Alpha = 0
	}
	sig.DepositedAt = now

	f.mu.Lock()
	sigs := f.byRes[sig.Resource]
	var out Signal
	merged := false
	for _, s := range sigs {
		if s.Agent == sig.Agent && s.Kind == sig.Kind {
			s.Intensity = math.Min(s.At(now)+sig.Intensity, maxIntensity)
			s.DepositedAt = now
			s.HalfLifeS = sig.HalfLifeS
			s.Decay = sig.Decay
			s.Alpha = sig.Alpha
			if sig.Note != "" {
				s.Note = sig.Note
			}
			if len(sig.Meta) > 0 {
				s.Meta = sig.Meta
			}
			out = *s
			merged = true
			break
		}
	}
	if !merged {
		sig.ID = newID()
		s := sig
		f.byRes[sig.Resource] = append(sigs, &s)
		out = sig
	}
	f.mu.Unlock()

	if f.onEvent != nil {
		f.onEvent("signal", out)
	}
	return out
}

// SniffQuery filters a read of the field.
type SniffQuery struct {
	Resource string  // exact resource, or ""
	Prefix   string  // resource prefix, or ""
	Kind     string  // signal kind, or ""
	Agent    string  // depositing agent, or ""
	Min      float64 // minimum current intensity (evaporated threshold if 0)
	Limit    int     // max signals returned (0 = 200)
}

// Sniff reads the field: returns matching signals with intensities decayed to
// now, strongest first.
func (f *Field) Sniff(q SniffQuery) []Signal {
	now := f.clock()
	min := q.Min
	if min <= 0 {
		min = evaporated
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}

	f.mu.RLock()
	var out []Signal
	scan := func(sigs []*Signal) {
		for _, s := range sigs {
			if q.Kind != "" && s.Kind != q.Kind {
				continue
			}
			if q.Agent != "" && s.Agent != q.Agent {
				continue
			}
			if cur := s.At(now); cur >= min {
				out = append(out, s.snapshot(now))
			}
		}
	}
	if q.Resource != "" {
		scan(f.byRes[q.Resource])
	} else {
		for res, sigs := range f.byRes {
			if q.Prefix != "" && !strings.HasPrefix(res, q.Prefix) {
				continue
			}
			scan(sigs)
		}
	}
	f.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Intensity > out[j].Intensity })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Hotspot is one entry of a gradient reading: a resource (or, at depth > 0,
// a whole subtree of resources) ranked by the summed current intensity of its
// signals.
type Hotspot struct {
	Resource  string             `json:"resource"`
	Total     float64            `json:"total"`
	Resources int                `json:"resources"` // distinct resources aggregated under this entry
	ByKind    map[string]float64 `json:"by_kind"`
	Agents    []string           `json:"agents"`
	TopSignal *Signal            `json:"top_signal,omitempty"`
}

// prefixAt returns a resource's ancestor at the given depth of its URI tree:
// depth counts path segments after the scheme, so for
// repo://src/auth/token.go depth 0 is "repo://", depth 1 "repo://src",
// depth 2 "repo://src/auth". Depth at or beyond the leaf, or depth < 0,
// returns the resource itself.
func prefixAt(resource string, depth int) string {
	if depth < 0 {
		return resource
	}
	scheme := ""
	rest := resource
	if i := strings.Index(resource, "://"); i >= 0 {
		scheme = resource[:i+3]
		rest = resource[i+3:]
	}
	segs := strings.Split(rest, "/")
	// drop trailing empty segment from a trailing slash
	for len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	if depth >= len(segs) {
		return resource
	}
	return scheme + strings.Join(segs[:depth], "/")
}

// Gradient aggregates the field into ranked hotspots so an agent can answer
// "where is the swarm's attention?" in one call. Filtering by kind gives
// kind-specific gradients ("where is help needed?", "what is gold right now?").
//
// depth < 0 ranks individual resources. depth >= 0 rolls signals up to that
// level of the URI tree, giving a self-similar coarse-to-fine view: an agent
// orients by sniffing the field at depth 1, descending into the hottest
// subtree at depth 2, and so on — O(tree depth) instead of O(resources).
func (f *Field) Gradient(prefix, kind string, k, depth int) []Hotspot {
	now := f.clock()
	if k <= 0 {
		k = 20
	}

	f.mu.RLock()
	agg := make(map[string]*Hotspot)
	members := make(map[string]map[string]struct{}) // group -> distinct resources
	for res, sigs := range f.byRes {
		if prefix != "" && !strings.HasPrefix(res, prefix) {
			continue
		}
		group := prefixAt(res, depth)
		for _, s := range sigs {
			if kind != "" && s.Kind != kind {
				continue
			}
			cur := s.At(now)
			if cur < evaporated {
				continue
			}
			h := agg[group]
			if h == nil {
				h = &Hotspot{Resource: group, ByKind: map[string]float64{}}
				agg[group] = h
				members[group] = map[string]struct{}{}
			}
			members[group][res] = struct{}{}
			h.Total += cur
			h.ByKind[s.Kind] += cur
			if !contains(h.Agents, s.Agent) {
				h.Agents = append(h.Agents, s.Agent)
			}
			if h.TopSignal == nil || cur > h.TopSignal.Intensity {
				snap := s.snapshot(now)
				h.TopSignal = &snap
			}
		}
	}
	f.mu.RUnlock()

	out := make([]Hotspot, 0, len(agg))
	for group, h := range agg {
		h.Resources = len(members[group])
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Sweep removes evaporated signals and empty resources. Returns the number of
// signals removed.
func (f *Field) Sweep() int {
	now := f.clock()
	removed := 0
	f.mu.Lock()
	for res, sigs := range f.byRes {
		kept := sigs[:0]
		for _, s := range sigs {
			if s.At(now) >= evaporated {
				kept = append(kept, s)
			} else {
				removed++
			}
		}
		if len(kept) == 0 {
			delete(f.byRes, res)
		} else {
			f.byRes[res] = kept
		}
	}
	f.mu.Unlock()
	return removed
}

// Stats summarizes the field for the manifest and observatory.
func (f *Field) Stats() (resources, signals int) {
	now := f.clock()
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, sigs := range f.byRes {
		live := 0
		for _, s := range sigs {
			if s.At(now) >= evaporated {
				live++
			}
		}
		if live > 0 {
			resources++
			signals += live
		}
	}
	return
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
