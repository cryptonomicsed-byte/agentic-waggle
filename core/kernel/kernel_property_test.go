package kernel

import (
	"math"
	"math/rand"
	"testing"
)

// These are property tests: instead of a handful of fixed cases, each runs
// thousands of randomized inputs and asserts an invariant that must hold for
// all of them. They are the practical (stateful-safe) half of round-2 item
// #2 — the two decay kernels also get a formal Lean proof of halving in
// verify/WaggleKernel.lean, but multi-channel interaction is too stateful for
// practical formal proof and lives here.

const trials = 20000

func rngFor(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// randDecay yields a plausible (decay, alpha) pair.
func randDecay(r *rand.Rand) (string, float64) {
	if r.Float64() < 0.5 {
		return "", 0
	}
	return "power", 0.3 + r.Float64()*3 // alpha in [0.3, 3.3]
}

// P1: decay never produces a negative or NaN intensity, and never exceeds the
// initial value (decay is monotone non-increasing in age).
func TestPropDecayBoundedNonNegative(t *testing.T) {
	r := rngFor(1)
	for i := 0; i < trials; i++ {
		i0 := r.Float64() * MaxIntensity
		hl := 1 + r.Float64()*100000
		age := r.Float64() * 1e6
		decay, alpha := randDecay(r)
		v := Decay(i0, hl, age, decay, alpha)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("decay produced non-finite: i0=%v hl=%v age=%v %s a=%v -> %v", i0, hl, age, decay, alpha, v)
		}
		if v < 0 {
			t.Fatalf("decay went negative: %v", v)
		}
		if v > i0+1e-9 {
			t.Fatalf("decay amplified: i0=%v -> %v (age=%v)", i0, v, age)
		}
	}
}

// P2: both kernels halve at exactly one half-life, for any alpha. This is the
// invariant the Lean spec proves; the property test guards the Go impl of it.
func TestPropHalvingParity(t *testing.T) {
	r := rngFor(2)
	for i := 0; i < trials; i++ {
		i0 := 0.01 + r.Float64()*MaxIntensity
		hl := 1 + r.Float64()*100000
		alpha := 0.3 + r.Float64()*3
		for _, d := range []struct {
			decay string
			alpha float64
		}{{"", 0}, {"power", alpha}} {
			got := Decay(i0, hl, hl, d.decay, d.alpha)
			want := i0 / 2
			if math.Abs(got-want) > 1e-9*i0+1e-12 {
				t.Fatalf("%s alpha=%v: at one half-life want %v, got %v", d.decay, d.alpha, want, got)
			}
		}
	}
}

// P3: the power tail is always heavier than (>=) the exponential tail past one
// half-life — the property that motivates using it for durable findings.
func TestPropPowerTailDominates(t *testing.T) {
	r := rngFor(3)
	for i := 0; i < trials; i++ {
		i0 := 0.01 + r.Float64()*MaxIntensity
		hl := 1 + r.Float64()*10000
		age := hl * (1 + r.Float64()*20) // strictly past one half-life
		alpha := 0.3 + r.Float64()*3
		exp := Decay(i0, hl, age, "", 0)
		pow := Decay(i0, hl, age, "power", alpha)
		if pow < exp-1e-9 {
			t.Fatalf("power tail below exp past half-life: pow=%v exp=%v (hl=%v age=%v a=%v)", pow, exp, hl, age, alpha)
		}
	}
}

// P4: cross-inhibition is always a multiplier in [floor, 1] — it can suppress
// but NEVER amplify. This is the runaway-feedback guard: no interaction can
// push a reading above its own un-inhibited value.
func TestPropInhibitionNeverAmplifies(t *testing.T) {
	r := rngFor(4)
	for i := 0; i < trials; i++ {
		mode := "low"
		if r.Float64() < 0.5 {
			mode = "high"
		}
		inten := r.Float64() * MaxIntensity * 1.5 // include out-of-range highs
		ref := 0.01 + r.Float64()
		floor := r.Float64()
		m := Inhibit(mode, inten, ref, floor)
		if m > 1+1e-12 {
			t.Fatalf("inhibition amplified: mode=%s I=%v ref=%v floor=%v -> %v", mode, inten, ref, floor, m)
		}
		cleanFloor := math.Max(0, math.Min(1, floor))
		if m < cleanFloor-1e-12 {
			t.Fatalf("inhibition below floor: mode=%s -> %v floor=%v", mode, m, cleanFloor)
		}
	}
}

// P5: composed inhibition (a chain of suppressors) still can't amplify — the
// product of multipliers each in [floor,1] stays in [0,1]. Directly models
// the taboo→gold→bounded interaction chain from v2.
func TestPropComposedInhibitionStaysBounded(t *testing.T) {
	r := rngFor(5)
	for i := 0; i < trials; i++ {
		mult := 1.0
		n := 1 + r.Intn(5)
		for j := 0; j < n; j++ {
			mode := []string{"high", "low"}[r.Intn(2)]
			mult *= Inhibit(mode, r.Float64()*MaxIntensity, 0.5, 0.1)
		}
		if mult > 1+1e-12 || mult < 0 {
			t.Fatalf("composed inhibition escaped [0,1]: %v", mult)
		}
	}
}

// P6: diffusion conserves mass — the bleed into a resource is at most
// DiffusionRate of the sibling mass, and is never negative. Diffusion can
// warm a neighborhood but cannot manufacture signal from nothing.
func TestPropDiffusionConserves(t *testing.T) {
	r := rngFor(6)
	for i := 0; i < trials; i++ {
		sib := (r.Float64() - 0.2) * 1000 // include negatives
		d := Diffusion(sib)
		if d < 0 {
			t.Fatalf("diffusion negative: sib=%v -> %v", sib, d)
		}
		if sib > 0 && d > DiffusionRate*sib+1e-9 {
			t.Fatalf("diffusion exceeded rate*mass: sib=%v -> %v", sib, d)
		}
	}
}

// P7: effective intensity is monotone in tier — a higher-tier signal of equal
// decayed strength and equal inhibition always reads at least as strong. Trust
// promotion can never make a signal read weaker.
func TestPropEffectiveMonotoneInTier(t *testing.T) {
	r := rngFor(7)
	for i := 0; i < trials; i++ {
		decayed := r.Float64() * MaxIntensity
		inhibition := r.Float64()
		for k := 1; k < len(EvidenceTiers); k++ {
			lo := Effective(decayed, TierWeight(EvidenceTiers[k-1]), inhibition)
			hi := Effective(decayed, TierWeight(EvidenceTiers[k]), inhibition)
			if hi < lo-1e-12 {
				t.Fatalf("effective not monotone in tier at %s->%s: %v < %v", EvidenceTiers[k-1], EvidenceTiers[k], hi, lo)
			}
		}
	}
}

// P8: AlphaFromValue always lands in [alphaMin, alphaMax] with positive
// exponent, so Decay can never receive an alpha that makes it diverge.
func TestPropAlphaFromValueClamped(t *testing.T) {
	r := rngFor(8)
	for i := 0; i < trials; i++ {
		amin := 0.1 + r.Float64()*2
		amax := amin + r.Float64()*3
		inten := (r.Float64() - 0.5) * MaxIntensity * 3 // include out-of-range
		a := AlphaFromValue(inten, amin, amax)
		if a < amin-1e-12 || a > amax+1e-12 || a <= 0 {
			t.Fatalf("alpha escaped [%v,%v] or non-positive: %v (I=%v)", amin, amax, a, inten)
		}
	}
}
