package main

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cryptonomicsed-byte/agentic/core/kernel"
)

// Signal is a single scent mark deposited on a resource by an agent.
// Its intensity decays exponentially with the configured half-life, so the
// field self-cleans: stale knowledge fades instead of accumulating as noise.
type Signal struct {
	ID        string  `json:"id"`
	Agent     string  `json:"agent"`
	Resource  string  `json:"resource"`
	Kind      string  `json:"kind"`              // the signal's channel; typed channels add defaults and semantics
	Subtype   string  `json:"subtype,omitempty"` // finer-grained label within a channel (e.g. compile stage)
	Intensity float64 `json:"intensity"`
	HalfLifeS float64 `json:"half_life_s"`
	Decay     string  `json:"decay,omitempty"` // "" or "exp" (exponential), "power" (heavy tail)
	Alpha     float64 `json:"alpha,omitempty"` // power-law exponent, default 1
	// EvidenceTier is where this signal sits on the trust ladder (see
	// EvidenceTiers). It never changes the stored intensity — read paths
	// weight by it, so promotion re-deposits at a higher tier instead of
	// rewriting history.
	EvidenceTier string            `json:"evidence_tier,omitempty"`
	Note         string            `json:"note,omitempty"`
	Meta         map[string]string `json:"meta,omitempty"`
	// TabooAuthenticated records, for taboo deposits only, whether the deposit
	// carried a valid Èṣù taboo-capability token. taboo is the one channel that
	// gates *actions* (it censors a path) rather than search efficiency, so
	// unlike the rest of the field it must be authenticatable. Tri-state via
	// pointer: nil = not a taboo (or no gate configured), false = taboo from an
	// unauthenticated source, true = taboo from a verified Ọbàtálá-lineage
	// capability. Surfaced by sniff_explain so a suppression's provenance is
	// auditable even during a transition period before enforcement is on.
	TabooAuthenticated *bool `json:"taboo_authenticated,omitempty"`
	// Cost is what producing this finding cost. A gold found for free and a
	// gold found after 10k tokens of reasoning are not equally attractive to
	// route toward; recording cost turns the field into an economic optimizer,
	// not just a discovery optimizer. Optional and additive across
	// reinforcement.
	Cost        *Cost     `json:"cost,omitempty"`
	DepositedAt time.Time `json:"deposited_at"`
	// Effective is the read-time weighted intensity (decay x tier weight x
	// cross-inhibition). Output-only: filled on snapshots served to clients,
	// never stored or journaled.
	Effective float64 `json:"effective_intensity,omitempty"`
	// CostEfficiency is effective intensity per unit cost, filled on reads
	// when the caller asks for cost-aware ranking. Output-only.
	CostEfficiency float64 `json:"cost_efficiency,omitempty"`
}

// Cost is the compute price of producing a finding. All fields optional; a
// depositor fills whichever it can meter. Reinforcement sums them, so a trail
// re-walked accumulates the total spend that keeps it hot.
type Cost struct {
	Tokens      float64 `json:"tokens,omitempty"`
	WallClockMS float64 `json:"wall_clock_ms,omitempty"`
	Dollars     float64 `json:"dollars,omitempty"`
}

// weight collapses a cost into one scalar for efficiency ranking. Dollars
// dominate when present (real money is the sharpest signal); else tokens; else
// wall-clock. A costless signal weighs the epsilon floor so it ranks as
// maximally efficient rather than dividing by zero.
func (c *Cost) weight() float64 {
	const eps = 1e-9
	if c == nil {
		return eps
	}
	// normalize onto a common ~"dollar" scale: tokens priced at $1/1e6 (a
	// round order-of-magnitude for LLM tokens), wall-clock at $1/hour.
	w := c.Dollars + c.Tokens/1e6 + c.WallClockMS/3.6e6
	if w < eps {
		return eps
	}
	return w
}

// add accumulates b into a (reinforcement), returning the merged cost.
func (a *Cost) add(b *Cost) *Cost {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &Cost{
		Tokens:      a.Tokens + b.Tokens,
		WallClockMS: a.WallClockMS + b.WallClockMS,
		Dollars:     a.Dollars + b.Dollars,
	}
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
	return kernel.Decay(s.Intensity, s.HalfLifeS, age, s.Decay, s.Alpha)
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
	evaporated = kernel.Evaporated
	// reinforcement can never push a signal above this ceiling
	maxIntensity = kernel.MaxIntensity
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
	mu       sync.RWMutex
	byRes    map[string][]*Signal
	clock    func() time.Time
	channels *Channels
	onEvent  func(kind string, payload any) // optional event sink (SSE hub / store)
}

func NewField() *Field {
	return &Field{byRes: make(map[string][]*Signal), clock: time.Now, channels: NewChannels()}
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
	ch, typed := f.channelOf(sig.Kind)
	if sig.Intensity <= 0 {
		sig.Intensity = DefaultIntensity
	}
	if sig.Intensity > maxIntensity {
		sig.Intensity = maxIntensity
	}
	if sig.HalfLifeS <= 0 {
		sig.HalfLifeS = DefaultHalfLifeS
		if typed && ch.DefaultHalfLifeS > 0 {
			sig.HalfLifeS = ch.DefaultHalfLifeS
		}
	}
	if sig.Decay != "power" {
		// a typed channel's registered kernel fills the gap only when the
		// depositor said nothing at all — an explicit choice (exp, or
		// anything unrecognized) still canonicalizes to exponential
		useChannelKernel := sig.Decay == "" && typed && ch.DecayKernel == "power"
		sig.Decay = "" // canonical exponential
		sig.Alpha = 0
		if useChannelKernel {
			sig.Decay = "power"
		}
	}
	if sig.Decay == "power" && sig.Alpha <= 0 && typed {
		if ch.AlphaFromValue && ch.AlphaMax > ch.AlphaMin && ch.AlphaMin > 0 {
			// confidence-weighted tail: a 10/10 bounded island gets the
			// heaviest tail and an instant escape the fastest — both halve
			// at one half-life
			sig.Alpha = kernel.AlphaFromValue(sig.Intensity, ch.AlphaMin, ch.AlphaMax)
		} else if ch.DefaultAlpha > 0 {
			sig.Alpha = ch.DefaultAlpha
		}
	}
	if !kernel.IsTier(sig.EvidenceTier) {
		sig.EvidenceTier = "self-report"
	}
	sig.Effective = 0
	sig.DepositedAt = now

	replace := typed && ch.Reinforce == "replace"
	f.mu.Lock()
	sigs := f.byRes[sig.Resource]
	var out Signal
	merged := false
	for _, s := range sigs {
		if s.Agent == sig.Agent && s.Kind == sig.Kind {
			if replace {
				// verdict semantics: a re-measurement sets the value —
				// measuring a region twice must not make it read stronger
				s.Intensity = sig.Intensity
			} else {
				s.Intensity = math.Min(s.At(now)+sig.Intensity, maxIntensity)
			}
			s.DepositedAt = now
			s.HalfLifeS = sig.HalfLifeS
			s.Decay = sig.Decay
			s.Alpha = sig.Alpha
			s.Subtype = sig.Subtype
			s.EvidenceTier = sig.EvidenceTier
			if replace {
				s.Cost = sig.Cost // a re-measurement's cost supersedes
			} else {
				s.Cost = s.Cost.add(sig.Cost) // reinforcement accumulates spend
			}
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

// channelOf looks up the typed channel behind a kind string. Unregistered
// kinds are untyped: classic exponential, additive, uninhibited signals.
func (f *Field) channelOf(kind string) (Channel, bool) {
	if f.channels == nil {
		return Channel{}, false
	}
	return f.channels.Get(kind)
}

// SniffQuery filters a read of the field.
type SniffQuery struct {
	Resource string  // exact resource, or ""
	Prefix   string  // resource prefix, or ""
	Kind     string  // signal kind, or ""
	Agent    string  // depositing agent, or ""
	Min      float64 // minimum current intensity (evaporated threshold if 0)
	MinTier  string  // minimum evidence tier ("corroborated" filters out self-reports)
	Optimize string  // "" ranks by intensity; "cost_efficiency" ranks by effective/cost
	Limit    int     // max signals returned (0 = 200)
}

// Sniff reads the field: returns matching signals with intensities decayed to
// now, strongest first. Every returned signal also carries its effective
// intensity: decay x evidence-tier weight x cross-inhibition from co-located
// signals, so a caller that wants the trust-adjusted picture doesn't need a
// second call.
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
	minRank := 0
	if q.MinTier != "" {
		minRank = TierRank(q.MinTier)
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
			if minRank > 0 && TierRank(s.EvidenceTier) < minRank {
				continue
			}
			if cur := s.At(now); cur >= min {
				snap := s.snapshot(now)
				m, _ := f.inhibitionAt(sigs, s, now)
				snap.Effective = kernel.Effective(cur, TierWeight(s.EvidenceTier), m)
				snap.CostEfficiency = snap.Effective / s.Cost.weight()
				out = append(out, snap)
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

	// Cost-aware routing prefers cheap findings: a gold found for free
	// outranks an equally strong gold that cost 10k tokens. Default ranking
	// stays by raw intensity.
	if q.Optimize == "cost_efficiency" {
		sort.Slice(out, func(i, j int) bool { return out[i].CostEfficiency > out[j].CostEfficiency })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Intensity > out[j].Intensity })
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// InhibitionTrace records one cross-inhibition applied to a signal, for
// sniff_explain: which co-located signal suppressed it, and by how much.
type InhibitionTrace struct {
	SourceKind string  `json:"source_kind"`
	SourceID   string  `json:"source_id"`
	Mode       string  `json:"mode"`
	Multiplier float64 `json:"multiplier"`
}

// inhibitionAt computes the combined cross-inhibition multiplier for signal
// target among its co-located signals. Caller holds at least a read lock.
//
// "high" mode: a strong inhibitor suppresses — m = max(floor, 1 - I/10)
// (taboo: the louder the exclusion, the colder nearby readings).
// "low" mode: a weak inhibitor suppresses — m = max(floor, min(1, (I/10)/ref))
// (bounded: gold inside a fragile escape zone reads with skepticism; absence
// of a bounded verdict is not evidence of fragility, so no signal, no
// penalty).
func (f *Field) inhibitionAt(sigs []*Signal, target *Signal, now time.Time) (float64, []InhibitionTrace) {
	if f.channels == nil {
		return 1, nil
	}
	inhibitors := f.channels.inhibitors(target.Kind)
	if len(inhibitors) == 0 {
		return 1, nil
	}
	mult := 1.0
	var traces []InhibitionTrace
	for _, in := range inhibitors {
		// strongest live inhibiting signal of that kind on this resource
		var strongest float64 = -1
		var strongestID string
		for _, s := range sigs {
			if s == target || s.Kind != in.Source {
				continue
			}
			if cur := s.At(now); cur >= evaporated && cur > strongest {
				strongest = cur
				strongestID = s.ID
			}
		}
		if strongest < 0 {
			continue
		}
		m := kernel.Inhibit(in.Mode, strongest, in.Ref, in.Floor)
		if m < 1 {
			mult *= m
			traces = append(traces, InhibitionTrace{SourceKind: in.Source, SourceID: strongestID, Mode: in.Mode, Multiplier: m})
		}
	}
	return mult, traces
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
	return f.GradientOpts(prefix, kind, k, depth, false, false)
}

// GradientOpts is Gradient with the two read-time refinements:
//
// weighted applies the trust math — each signal contributes its effective
// intensity (decay x evidence-tier weight x cross-inhibition) instead of the
// raw decayed value, so a self-reported gold inside a fragile bounded zone
// counts for a fraction of an on-chain-anchored one on a robust island.
//
// diffuse adds the spatial bleed (leaf level only): each resource picks up 5%
// of its siblings' total under the same parent, so a hot neighborhood warms
// resources that have no signals of their own yet. Diffusion is computed
// lazily at read time — nothing rewrites stored intensities, and journal
// replay is untouched.
func (f *Field) GradientOpts(prefix, kind string, k, depth int, weighted, diffuse bool) []Hotspot {
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
			contrib := cur
			if weighted {
				m, _ := f.inhibitionAt(sigs, s, now)
				contrib = cur * TierWeight(s.EvidenceTier) * m
			}
			h := agg[group]
			if h == nil {
				h = &Hotspot{Resource: group, ByKind: map[string]float64{}}
				agg[group] = h
				members[group] = map[string]struct{}{}
			}
			members[group][res] = struct{}{}
			h.Total += contrib
			h.ByKind[s.Kind] += contrib
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

	if diffuse && depth < 0 {
		// 5% sibling bleed under a shared parent, applied on the aggregated
		// totals: bleed(r) = diffusionRate * (parent total - own total)
		parentTotals := make(map[string]float64)
		for res, h := range agg {
			parentTotals[parentOf(res)] += h.Total
		}
		for res, h := range agg {
			if bleed := kernel.Diffusion(parentTotals[parentOf(res)] - h.Total); bleed > 0 {
				h.Total += bleed
			}
		}
	}

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

// diffusionRate is the fraction of sibling intensity that bleeds into a
// resource when a gradient is read with diffuse=1.
const diffusionRate = kernel.DiffusionRate

// parentOf returns a resource's immediate parent in its URI tree (the prefix
// one segment above the leaf), or the resource itself if it has no parent.
func parentOf(resource string) string {
	rest := resource
	if i := strings.Index(resource, "://"); i >= 0 {
		rest = resource[i+3:]
	}
	segs := strings.Split(strings.TrimRight(rest, "/"), "/")
	if len(segs) <= 1 {
		return resource
	}
	return prefixAt(resource, len(segs)-1)
}

// Contribution is one signal's line in a sniff_explain reading: the raw
// decayed value and every factor between it and the effective number.
type Contribution struct {
	Signal      Signal            `json:"signal"`
	TierWeight  float64           `json:"tier_weight"`
	Inhibitions []InhibitionTrace `json:"inhibitions,omitempty"`
	Effective   float64           `json:"effective"`
}

// Explanation answers "why does this resource read the way it does": every
// live signal with its evidence tier, tier weight, the cross-inhibitions
// suppressing it and the resulting effective intensity, plus raw and
// effective totals and the ambient diffusion from siblings.
type Explanation struct {
	Resource        string             `json:"resource"`
	TotalRaw        float64            `json:"total_raw"`
	TotalEffective  float64            `json:"total_effective"`
	ByKindRaw       map[string]float64 `json:"by_kind_raw"`
	ByKindEffective map[string]float64 `json:"by_kind_effective"`
	Diffusion       float64            `json:"diffusion"`
	Contributions   []Contribution     `json:"contributions"`
}

// Explain is the read path behind the sniff_explain verb.
func (f *Field) Explain(resource string) Explanation {
	now := f.clock()
	ex := Explanation{
		Resource:        resource,
		ByKindRaw:       map[string]float64{},
		ByKindEffective: map[string]float64{},
	}

	f.mu.RLock()
	sigs := f.byRes[resource]
	for _, s := range sigs {
		cur := s.At(now)
		if cur < evaporated {
			continue
		}
		w := TierWeight(s.EvidenceTier)
		m, traces := f.inhibitionAt(sigs, s, now)
		eff := cur * w * m
		snap := s.snapshot(now)
		snap.Effective = eff
		ex.Contributions = append(ex.Contributions, Contribution{
			Signal: snap, TierWeight: w, Inhibitions: traces, Effective: eff,
		})
		ex.TotalRaw += cur
		ex.TotalEffective += eff
		ex.ByKindRaw[s.Kind] += cur
		ex.ByKindEffective[s.Kind] += eff
	}
	// ambient warmth from siblings under the same parent
	parent := parentOf(resource)
	if parent != resource {
		siblingTotal := 0.0
		for res, ss := range f.byRes {
			if res == resource || parentOf(res) != parent {
				continue
			}
			for _, s := range ss {
				if cur := s.At(now); cur >= evaporated {
					siblingTotal += cur
				}
			}
		}
		ex.Diffusion = kernel.Diffusion(siblingTotal)
	}
	f.mu.RUnlock()

	sort.Slice(ex.Contributions, func(i, j int) bool {
		return ex.Contributions[i].Effective > ex.Contributions[j].Effective
	})
	return ex
}

// BatchRollup answers a multi-URI sniff in one pass: for each requested URI,
// the summed live intensity of every signal at or under it (URI treated as a
// subtree prefix), so a depth-first explorer prices N candidate branches for
// one round-trip instead of N.
func (f *Field) BatchRollup(uris []string, kind string, weighted bool) map[string]Hotspot {
	now := f.clock()
	out := make(map[string]*Hotspot, len(uris))
	members := make(map[string]map[string]struct{}, len(uris))
	for _, u := range uris {
		out[u] = &Hotspot{Resource: u, ByKind: map[string]float64{}}
		members[u] = map[string]struct{}{}
	}

	f.mu.RLock()
	for res, sigs := range f.byRes {
		for _, u := range uris {
			// same prefix semantics as sniff/gradient
			if !strings.HasPrefix(res, u) {
				continue
			}
			h := out[u]
			for _, s := range sigs {
				if kind != "" && s.Kind != kind {
					continue
				}
				cur := s.At(now)
				if cur < evaporated {
					continue
				}
				contrib := cur
				if weighted {
					m, _ := f.inhibitionAt(sigs, s, now)
					contrib = cur * TierWeight(s.EvidenceTier) * m
				}
				members[u][res] = struct{}{}
				h.Total += contrib
				h.ByKind[s.Kind] += contrib
				if !contains(h.Agents, s.Agent) {
					h.Agents = append(h.Agents, s.Agent)
				}
				if h.TopSignal == nil || cur > h.TopSignal.Intensity {
					snap := s.snapshot(now)
					h.TopSignal = &snap
				}
			}
		}
	}
	f.mu.RUnlock()

	result := make(map[string]Hotspot, len(uris))
	for u, h := range out {
		h.Resources = len(members[u])
		result[u] = *h
	}
	return result
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
