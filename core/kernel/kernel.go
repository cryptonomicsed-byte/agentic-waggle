// Package kernel is the pure, side-effect-free math at the heart of the
// Waggle field: decay, cross-inhibition, diffusion and the evidence-tier
// weighting. Nothing here touches I/O, time, locks or global state — every
// function is a total function of its arguments.
//
// That isolation is the point (Connection Map v2 round 2, #2): pure functions
// can be property-tested exhaustively and formally specified. The invariants
// the property tests assert, and the Lean spec proves for the two decay
// kernels, all live against this package. The daemon (package main) calls
// into here so the tested math and the running math are the same math.
package kernel

import "math"

// Evaporated is the intensity floor: signals below it are invisible and swept.
const Evaporated = 0.01

// MaxIntensity is the reinforcement ceiling and the normalizer for values
// expressed on the field's native 0..10 scale.
const MaxIntensity = 10.0

// DiffusionRate is the fraction of a sibling group's intensity that bleeds
// into a resource on a diffuse read.
const DiffusionRate = 0.05

// Decay returns the intensity of a signal of initial strength `intensity` and
// half-life `halfLifeS` at age `age` seconds, under the selected kernel.
//
//   - "power": heavy-tailed, intensity * (1 + age/scale)^-alpha, with
//     scale = halfLifeS / (2^(1/alpha) - 1) so it halves at exactly one
//     half-life. alpha <= 0 defaults to 1.
//   - anything else: exponential, intensity * 2^(-age/halfLifeS).
//
// Both kernels agree at age 0 and at one half-life. This is the one function
// the Lean spec proves the halving property for; see verify/WaggleKernel.lean.
func Decay(intensity, halfLifeS, age float64, decay string, alpha float64) float64 {
	if age <= 0 {
		return intensity
	}
	if halfLifeS <= 0 {
		return intensity
	}
	if decay == "power" {
		if alpha <= 0 {
			alpha = 1
		}
		scale := halfLifeS / (math.Pow(2, 1/alpha) - 1)
		return intensity * math.Pow(1+age/scale, -alpha)
	}
	return intensity * math.Exp2(-age/halfLifeS)
}

// AlphaFromValue maps a deposit's intensity to a confidence-weighted power-law
// exponent: alpha = alphaMax - (alphaMax-alphaMin) * intensity/MaxIntensity.
// The bounded channel uses this so a confident robustness verdict decays on
// the heaviest tail and an instant-escape verdict fades fastest — both still
// halving at one half-life because Decay's scale is derived from alpha.
//
// The result is clamped to [alphaMin, alphaMax] so an out-of-range intensity
// can never produce a non-positive exponent (which would make Decay diverge).
func AlphaFromValue(intensity, alphaMin, alphaMax float64) float64 {
	if alphaMax <= alphaMin || alphaMin <= 0 {
		return 1
	}
	a := alphaMax - (alphaMax-alphaMin)*(intensity/MaxIntensity)
	if a < alphaMin {
		return alphaMin
	}
	if a > alphaMax {
		return alphaMax
	}
	return a
}

// Inhibit is the cross-inhibition multiplier one channel applies to another's
// read-time weight, given the strongest live inhibiting intensity.
//
//   - "high": a strong inhibitor suppresses — m = max(floor, 1 - I/10).
//     Taboo: the louder the exclusion, the colder nearby readings.
//   - "low": a weak inhibitor suppresses — m = max(floor, min(1, (I/10)/ref)).
//     Bounded: gold inside a fragile escape zone is down-weighted; gold on a
//     robust island (I/10 >= ref) is not.
//
// The result is always in [floor, 1]: suppression is skepticism, never
// erasure, and can never amplify (a multiplier > 1 would let inhibition
// *increase* a reading, which is the runaway-feedback case #2 guards against).
func Inhibit(mode string, inhibitorIntensity, ref, floor float64) float64 {
	if floor < 0 {
		floor = 0
	}
	if floor > 1 {
		floor = 1
	}
	norm := inhibitorIntensity / MaxIntensity
	if norm < 0 {
		norm = 0
	}
	var m float64
	if mode == "high" {
		m = 1 - norm
	} else {
		if ref <= 0 {
			ref = 0.5
		}
		m = norm / ref
	}
	if m < floor {
		m = floor
	}
	if m > 1 {
		m = 1
	}
	return m
}

// Diffusion is the ambient warmth a resource picks up from its siblings: a
// flat DiffusionRate of the surrounding intensity. It is strictly additive
// and strictly bounded by the sibling mass — diffusion moves no more than
// DiffusionRate of what already exists, so it can never create net signal
// mass out of nothing (the conservation invariant #2 checks).
func Diffusion(siblingIntensity float64) float64 {
	if siblingIntensity <= 0 {
		return 0
	}
	return DiffusionRate * siblingIntensity
}

// EvidenceTiers is the trust ladder, weakest to strongest.
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

// TierWeight is the read-time weight of an evidence tier; unknown/empty tiers
// weigh as self-report.
func TierWeight(tier string) float64 {
	if w, ok := tierWeights[tier]; ok {
		return w
	}
	return tierWeights["self-report"]
}

// IsTier reports whether tier is a known rung of the ladder.
func IsTier(tier string) bool {
	_, ok := tierWeights[tier]
	return ok
}

// TierRank orders tiers for min-tier filtering; unknown tiers rank 0.
func TierRank(tier string) int {
	for i, t := range EvidenceTiers {
		if t == tier {
			return i
		}
	}
	return 0
}

// Effective composes the full read-time value of a signal: decayed intensity,
// weighted by evidence tier, suppressed by the combined cross-inhibition
// multiplier. This is the number sniff and weighted gradients rank by, and
// the identity the conformance test vectors pin.
func Effective(decayed, tierWeight, inhibition float64) float64 {
	return decayed * tierWeight * inhibition
}
