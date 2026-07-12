package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/cryptonomicsed-byte/agentic/core/kernel"
)

// TestConformanceVectors runs the language-agnostic protocol conformance
// vectors (docs/SPEC/vectors/kernel-v1.json) against this implementation's
// pure kernel. A re-implementation in any language ports this runner and must
// match every `Expected` to 1e-9 relative — the same way web standards ship
// test vectors. For the reference implementation it guards against silent
// numeric drift between the spec and the code.
func TestConformanceVectors(t *testing.T) {
	path := filepath.Join("..", "docs", "SPEC", "vectors", "kernel-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}

	var doc struct {
		Decay struct {
			Cases []struct {
				Intensity, HalfLife, Age float64
				Decay                    string
				Alpha, Expected          float64
			}
		}
		Inhibition struct {
			Cases []struct {
				Mode                            string
				Inhibitor, Ref, Floor, Expected float64
			}
		}
		Tiers struct {
			Cases []struct {
				Tier   string
				Weight float64
				Rank   int
			}
		}
		Effective struct {
			Cases []struct {
				Decayed, TierWeight, Inhibition, Expected float64
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}

	close := func(got, want float64) bool {
		return math.Abs(got-want) <= 1e-9*math.Max(1, math.Abs(want))
	}

	if len(doc.Decay.Cases) == 0 || len(doc.Inhibition.Cases) == 0 ||
		len(doc.Tiers.Cases) == 0 || len(doc.Effective.Cases) == 0 {
		t.Fatal("conformance vectors incomplete")
	}

	for i, c := range doc.Decay.Cases {
		got := kernel.Decay(c.Intensity, c.HalfLife, c.Age, c.Decay, c.Alpha)
		if !close(got, c.Expected) {
			t.Errorf("decay[%d] (%v,%v,%v,%q,%v): got %v want %v", i, c.Intensity, c.HalfLife, c.Age, c.Decay, c.Alpha, got, c.Expected)
		}
	}
	for i, c := range doc.Inhibition.Cases {
		got := kernel.Inhibit(c.Mode, c.Inhibitor, c.Ref, c.Floor)
		if !close(got, c.Expected) {
			t.Errorf("inhibition[%d] (%q,%v,%v,%v): got %v want %v", i, c.Mode, c.Inhibitor, c.Ref, c.Floor, got, c.Expected)
		}
	}
	for i, c := range doc.Tiers.Cases {
		if got := kernel.TierWeight(c.Tier); !close(got, c.Weight) {
			t.Errorf("tier[%d] %q weight: got %v want %v", i, c.Tier, got, c.Weight)
		}
		if got := kernel.TierRank(c.Tier); got != c.Rank {
			t.Errorf("tier[%d] %q rank: got %v want %v", i, c.Tier, got, c.Rank)
		}
	}
	for i, c := range doc.Effective.Cases {
		got := kernel.Effective(c.Decayed, c.TierWeight, c.Inhibition)
		if !close(got, c.Expected) {
			t.Errorf("effective[%d]: got %v want %v", i, got, c.Expected)
		}
	}
}
