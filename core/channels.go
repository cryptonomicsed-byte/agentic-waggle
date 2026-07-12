package main

import (
	"sort"
	"sync"
)

// Inhibition declares that signals on one channel suppress the read-time
// weight of another channel's signals on the same resource. Suppression is
// skepticism, never erasure: the multiplier is floored, so a genuinely
// persistent trail can climb back through reinforcement.
type Inhibition struct {
	// Channel is the target channel whose readings are suppressed.
	Channel string `json:"channel"`
	// Mode selects the direction: "high" means a strong inhibitor suppresses
	// (taboo — the louder the exclusion, the colder nearby gold reads);
	// "low" means a weak inhibitor suppresses (bounded — gold inside a
	// fragile escape zone is down-weighted, gold on a robust island is not).
	Mode string `json:"mode"`
	// Ref is the normalized intensity (0-1) at which "low" mode stops
	// suppressing. Default 0.5: at or above the fragile boundary, no penalty.
	Ref float64 `json:"ref,omitempty"`
	// Floor is the minimum multiplier suppression can reach.
	Floor float64 `json:"floor"`
}

// Channel is a typed signal kind: decay defaults, reinforcement semantics and
// cross-inhibitions self-registered once instead of re-implemented by every
// depositor. The kind string on a signal is its channel name; unregistered
// kinds behave exactly as before (exponential, additive, no inhibitions), so
// the flat-string vocabulary keeps working.
type Channel struct {
	Name     string   `json:"name"`
	Doc      string   `json:"doc,omitempty"`
	Subtypes []string `json:"subtypes,omitempty"`
	// Deposit defaults, applied only when the deposit omits them.
	DefaultHalfLifeS float64 `json:"default_half_life_s,omitempty"`
	DecayKernel      string  `json:"decay_kernel,omitempty"` // "exp" or "power"
	DefaultAlpha     float64 `json:"default_alpha,omitempty"`
	// AlphaFromValue scales the power-law exponent with the deposit's
	// normalized intensity: alpha = AlphaMax - (AlphaMax-AlphaMin) * I/10.
	// The bounded channel uses this so confident robustness verdicts decay
	// on the heaviest tail while escape verdicts fade fastest — both ends
	// still halve at exactly one half-life.
	AlphaFromValue bool    `json:"alpha_from_value,omitempty"`
	AlphaMin       float64 `json:"alpha_min,omitempty"`
	AlphaMax       float64 `json:"alpha_max,omitempty"`
	// Reinforce is "add" (default: pheromone reinforcement, deposits stack)
	// or "replace" (verdict semantics: a re-measurement sets the value —
	// two s=0.3 scans must not read as s=0.6).
	Reinforce     string       `json:"reinforce,omitempty"`
	CrossInhibits []Inhibition `json:"cross_inhibits,omitempty"`
}

// Evidence tiers, weakest to strongest. The ladder is the one trust model
// every consumer reads instead of inventing its own: a self-reported finding
// weighs a fifth of an on-chain-anchored one at read time, and promotion
// (corroboration, Zangbeto verification, Sui anchoring) re-deposits at a
// higher tier rather than rewriting history.
var EvidenceTiers = []string{
	"self-report",
	"corroborated",
	"watch-derived",
	"zangbeto-verified",
	"on-chain-anchored",
}

var tierWeights = map[string]float64{
	"self-report":       0.2,
	"corroborated":      0.4,
	"watch-derived":     0.6,
	"zangbeto-verified": 0.8,
	"on-chain-anchored": 1.0,
}

// TierWeight returns the read-time weight of an evidence tier. Unknown or
// empty tiers weigh as self-report: an unverified claim is an unverified
// claim no matter how it is spelled.
func TierWeight(tier string) float64 {
	if w, ok := tierWeights[tier]; ok {
		return w
	}
	return tierWeights["self-report"]
}

// TierRank orders tiers for min_tier filtering. Unknown tiers rank 0.
func TierRank(tier string) int {
	for i, t := range EvidenceTiers {
		if t == tier {
			return i
		}
	}
	return 0
}

// Channels is the registry behind the manifest's channels block. Registering
// a channel is how a new power teaches the substrate its signal type — zero
// Go source changes per power.
type Channels struct {
	mu     sync.RWMutex
	byName map[string]*Channel
}

// builtinChannels covers the classic vocabulary plus the typed channels the
// ecosystem shares: bounded (Mandelbrot robustness verdicts), taboo
// (Ọbàtálá's ethical exclusions) and federation-health (Vantage bridges).
func builtinChannels() []Channel {
	return []Channel{
		{Name: "explored", Doc: "an agent has looked at this resource"},
		{Name: "claimed", Doc: "informational echo of a lease"},
		{Name: "gold", Doc: "high-value finding here; others should look. Pass decay=power for findings that should fade to background, not to nothing."},
		{Name: "dead-end", Doc: "this path was tried and failed; avoid repeating it"},
		{Name: "help", Doc: "an agent is stuck here and requests assistance"},
		{Name: "warn", Doc: "hazard: destructive, flaky, or costly territory"},
		{Name: "handoff", Doc: "context parked here for a successor agent"},
		{Name: "heartbeat", Doc: "liveness trace of a long-running agent"},
		{Name: "bounded",
			Doc:              "Mandelbrot robustness verdict: intensity is 10x the stability score (0 = escapes immediately, 10 = deep bounded island). Replace-mode: a re-measurement sets the value. Confident islands decay on the heaviest tail.",
			DefaultHalfLifeS: 7200, DecayKernel: "power",
			AlphaFromValue: true, AlphaMin: 0.5, AlphaMax: 2.0,
			Reinforce: "replace",
			CrossInhibits: []Inhibition{
				{Channel: "gold", Mode: "low", Ref: 0.5, Floor: 0.25},
			}},
		{Name: "taboo",
			Doc:              "ethical exclusion (Ọbàtálá): slow-decay suppression of a territory with the justification in meta. Not a dead-end — a judgment. Suppresses gold readings nearby.",
			DefaultHalfLifeS: 86400, DecayKernel: "power", DefaultAlpha: 0.5,
			CrossInhibits: []Inhibition{
				{Channel: "gold", Mode: "high", Floor: 0.1},
				{Channel: "bounded", Mode: "high", Floor: 0.25},
			}},
		{Name: "federation-health",
			Doc:              "Vantage bridge liveness/latency meta-signal: sniff before trusting imported foreign gold.",
			DefaultHalfLifeS: 600},
	}
}

func NewChannels() *Channels {
	c := &Channels{byName: make(map[string]*Channel)}
	for _, ch := range builtinChannels() {
		cp := ch
		c.byName[ch.Name] = &cp
	}
	return c
}

// Register adds or replaces a channel definition. Sanity-clamps floors and
// modes so a bad registration degrades to "no inhibition" rather than
// zeroing the field.
func (c *Channels) Register(ch Channel) Channel {
	if ch.Reinforce != "replace" {
		ch.Reinforce = "add"
	}
	if ch.DecayKernel != "power" && ch.DecayKernel != "exp" {
		ch.DecayKernel = ""
	}
	cleaned := ch.CrossInhibits[:0]
	for _, in := range ch.CrossInhibits {
		if in.Channel == "" || (in.Mode != "high" && in.Mode != "low") {
			continue
		}
		if in.Floor < 0 || in.Floor > 1 {
			in.Floor = 0.25
		}
		if in.Mode == "low" && (in.Ref <= 0 || in.Ref > 1) {
			in.Ref = 0.5
		}
		cleaned = append(cleaned, in)
	}
	ch.CrossInhibits = cleaned
	c.mu.Lock()
	cp := ch
	c.byName[ch.Name] = &cp
	c.mu.Unlock()
	return ch
}

func (c *Channels) Get(name string) (Channel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ch, ok := c.byName[name]; ok {
		return *ch, true
	}
	return Channel{}, false
}

func (c *Channels) List() []Channel {
	c.mu.RLock()
	out := make([]Channel, 0, len(c.byName))
	for _, ch := range c.byName {
		out = append(out, *ch)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// inhibitors returns every (channel, inhibition) pair that targets the given
// channel, so read paths can ask "who suppresses gold?" in one call.
func (c *Channels) inhibitors(target string) []struct {
	Source string
	Inhibition
} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []struct {
		Source string
		Inhibition
	}
	for name, ch := range c.byName {
		for _, in := range ch.CrossInhibits {
			if in.Channel == target {
				out = append(out, struct {
					Source string
					Inhibition
				}{name, in})
			}
		}
	}
	return out
}
