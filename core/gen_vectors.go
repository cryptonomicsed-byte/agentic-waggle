//go:build ignore

// gen_vectors.go emits kernel-v1.json from the reference kernel. Run from the
// core/ module directory to regenerate the conformance vectors after an
// intentional, spec-versioned change to the math:
//
//	cd core && go run gen_vectors.go > ../docs/SPEC/vectors/kernel-v1.json
//
// The vectors are the frozen expected outputs; a re-implementation matches
// them, it does not regenerate them. Only the reference implementation
// regenerates, and only alongside a spec version bump.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/cryptonomicsed-byte/agentic/core/kernel"
)

func main() {
	type dcase struct {
		Intensity, HalfLife, Age float64
		Decay                    string
		Alpha, Expected          float64
	}
	var decay []dcase
	add := func(i, h, a float64, k string, al float64) {
		decay = append(decay, dcase{i, h, a, k, al, kernel.Decay(i, h, a, k, al)})
	}
	add(4, 60, 0, "", 0)
	add(4, 60, 60, "", 0)
	add(4, 60, 120, "", 0)
	add(4, 60, 600, "", 0)
	add(4, 60, 0, "power", 1)
	add(4, 60, 60, "power", 1)
	add(4, 60, 600, "power", 1)
	add(8, 7200, 7200, "power", 0.8)
	add(8, 7200, 72000, "power", 0.8)
	add(10, 1800, 3600, "power", 2)
	add(2, 300, 300, "power", 0.5)

	type icase struct {
		Mode                            string
		Inhibitor, Ref, Floor, Expected float64
	}
	var inh []icase
	addi := func(m string, x, r, f float64) {
		inh = append(inh, icase{m, x, r, f, kernel.Inhibit(m, x, r, f)})
	}
	addi("low", 1, 0.5, 0.25)
	addi("low", 9, 0.5, 0.25)
	addi("low", 5, 0.5, 0.25)
	addi("high", 9, 0, 0.1)
	addi("high", 3, 0, 0.1)
	addi("high", 10, 0, 0.25)

	type tcase struct {
		Tier   string
		Weight float64
		Rank   int
	}
	var tiers []tcase
	for _, t := range kernel.EvidenceTiers {
		tiers = append(tiers, tcase{t, kernel.TierWeight(t), kernel.TierRank(t)})
	}
	tiers = append(tiers, tcase{"bogus", kernel.TierWeight("bogus"), kernel.TierRank("bogus")})

	type ecase struct{ Decayed, TierWeight, Inhibition, Expected float64 }
	eff := []ecase{
		{8, 0.6, 0.25, kernel.Effective(8, 0.6, 0.25)},
		{5, 1.0, 1.0, kernel.Effective(5, 1, 1)},
		{6, 0.2, 0.1, kernel.Effective(6, 0.2, 0.1)},
	}

	out := map[string]any{
		"description": "waggle/v1 conformance vectors — pure kernel outputs pinned to 1e-9. See docs/SPEC/waggle-v1.md §12.",
		"version":     "1.0.0",
		"decay":       map[string]any{"description": "decay kernels: exponential and power-law, halving at one half-life", "cases": decay},
		"inhibition":  map[string]any{"description": "cross-inhibition multipliers, high (taboo) and low (bounded/dead-cat) modes", "cases": inh},
		"tiers":       map[string]any{"description": "evidence-tier weights and ranks", "cases": tiers},
		"effective":   map[string]any{"description": "effective = decayed * tier_weight * inhibition", "cases": eff},
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
