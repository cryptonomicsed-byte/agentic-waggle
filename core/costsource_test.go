package main

import "testing"

// Cost provenance must survive reinforcement honestly: one producer's costs keep
// their source, costs summed across producers collapse to "mixed" so a blended
// efficiency number is never mistaken for a clean single-instrument one.
func TestCostSourceMerge(t *testing.T) {
	loom := &CostSource{Producer: "loom", Method: "commission", Units: "dollars"}
	loom2 := &CostSource{Producer: "loom", Method: "commission", Units: "dollars"}
	osovm := &CostSource{Producer: "osovm", Method: "compile-wall-clock", Units: "ms"}

	cases := []struct {
		name       string
		a, b       *Cost
		wantProd   string
		wantTokens float64
	}{
		{"nil+sourced", nil, &Cost{Tokens: 5, Source: loom}, "loom", 5},
		{"sourced+nil", &Cost{Tokens: 5, Source: loom}, nil, "loom", 5},
		{"same producer keeps source",
			&Cost{Dollars: 1, Source: loom}, &Cost{Dollars: 2, Source: loom2}, "loom", 0},
		{"different producers → mixed",
			&Cost{Dollars: 1, Source: loom}, &Cost{WallClockMS: 9, Source: osovm}, "mixed", 0},
		{"one side unsourced keeps the other",
			&Cost{Tokens: 1, Source: loom}, &Cost{Tokens: 2}, "loom", 3},
	}
	for _, c := range cases {
		got := c.a.add(c.b)
		if got.Source == nil {
			t.Fatalf("%s: nil source", c.name)
		}
		if got.Source.Producer != c.wantProd {
			t.Errorf("%s: producer = %q, want %q", c.name, got.Source.Producer, c.wantProd)
		}
		if c.wantTokens != 0 && got.Tokens != c.wantTokens {
			t.Errorf("%s: tokens = %v, want %v", c.name, got.Tokens, c.wantTokens)
		}
	}

	// summed numbers accumulate even when provenance goes mixed
	merged := (&Cost{Dollars: 1, Source: loom}).add(&Cost{WallClockMS: 9, Source: osovm})
	if merged.Dollars != 1 || merged.WallClockMS != 9 {
		t.Errorf("mixed merge dropped a component: %+v", merged)
	}
}
