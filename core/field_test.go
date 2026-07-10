package main

import (
	"math"
	"testing"
	"time"
)

// fakeClock lets tests advance time deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestField() (*Field, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	f := NewField()
	f.clock = clk.now
	return f, clk
}

func TestDecayHalvesAtHalfLife(t *testing.T) {
	f, clk := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "repo://x", Kind: "explored", Intensity: 4, HalfLifeS: 60})

	clk.advance(60 * time.Second)
	sigs := f.Sniff(SniffQuery{Resource: "repo://x"})
	if len(sigs) != 1 {
		t.Fatalf("want 1 signal, got %d", len(sigs))
	}
	if got := sigs[0].Intensity; math.Abs(got-2) > 1e-9 {
		t.Fatalf("after one half-life want intensity 2, got %v", got)
	}

	clk.advance(60 * time.Second)
	sigs = f.Sniff(SniffQuery{Resource: "repo://x"})
	if got := sigs[0].Intensity; math.Abs(got-1) > 1e-9 {
		t.Fatalf("after two half-lives want intensity 1, got %v", got)
	}
}

func TestEvaporation(t *testing.T) {
	f, clk := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "repo://x", Kind: "explored", Intensity: 1, HalfLifeS: 10})

	clk.advance(200 * time.Second) // 20 half-lives: 1 * 2^-20 ≈ 1e-6
	if sigs := f.Sniff(SniffQuery{Resource: "repo://x"}); len(sigs) != 0 {
		t.Fatalf("evaporated signal still sniffable: %+v", sigs)
	}
	if removed := f.Sweep(); removed != 1 {
		t.Fatalf("sweep want 1 removed, got %d", removed)
	}
	if res, sig := f.Stats(); res != 0 || sig != 0 {
		t.Fatalf("stats after sweep want 0/0, got %d/%d", res, sig)
	}
}

func TestReinforcementMergesAndResetsClock(t *testing.T) {
	f, clk := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "repo://x", Kind: "gold", Intensity: 2, HalfLifeS: 60})
	clk.advance(60 * time.Second) // decays to 1

	out := f.Deposit(Signal{Agent: "a1", Resource: "repo://x", Kind: "gold", Intensity: 3, HalfLifeS: 60})
	if math.Abs(out.Intensity-4) > 1e-9 { // 1 (decayed) + 3 (deposit)
		t.Fatalf("reinforced intensity want 4, got %v", out.Intensity)
	}
	if sigs := f.Sniff(SniffQuery{Resource: "repo://x"}); len(sigs) != 1 {
		t.Fatalf("reinforcement must merge, got %d signals", len(sigs))
	}

	// distinct kind or agent must NOT merge
	f.Deposit(Signal{Agent: "a1", Resource: "repo://x", Kind: "explored"})
	f.Deposit(Signal{Agent: "a2", Resource: "repo://x", Kind: "gold"})
	if sigs := f.Sniff(SniffQuery{Resource: "repo://x"}); len(sigs) != 3 {
		t.Fatalf("want 3 distinct signals, got %d", len(sigs))
	}
}

func TestIntensityCeiling(t *testing.T) {
	f, _ := newTestField()
	for i := 0; i < 10; i++ {
		f.Deposit(Signal{Agent: "a1", Resource: "repo://x", Kind: "gold", Intensity: 5})
	}
	sigs := f.Sniff(SniffQuery{Resource: "repo://x"})
	if sigs[0].Intensity > maxIntensity {
		t.Fatalf("intensity %v exceeds ceiling %v", sigs[0].Intensity, maxIntensity)
	}
}

func TestSniffFilters(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "repo://src/auth.go", Kind: "explored"})
	f.Deposit(Signal{Agent: "a2", Resource: "repo://src/db.go", Kind: "dead-end"})
	f.Deposit(Signal{Agent: "a2", Resource: "docs://readme", Kind: "explored"})

	if got := len(f.Sniff(SniffQuery{Prefix: "repo://"})); got != 2 {
		t.Fatalf("prefix filter want 2, got %d", got)
	}
	if got := len(f.Sniff(SniffQuery{Kind: "dead-end"})); got != 1 {
		t.Fatalf("kind filter want 1, got %d", got)
	}
	if got := len(f.Sniff(SniffQuery{Agent: "a2"})); got != 2 {
		t.Fatalf("agent filter want 2, got %d", got)
	}
}

func TestGradientRanksBySummedIntensity(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "repo://hot", Kind: "gold", Intensity: 5})
	f.Deposit(Signal{Agent: "a2", Resource: "repo://hot", Kind: "explored", Intensity: 3})
	f.Deposit(Signal{Agent: "a1", Resource: "repo://cold", Kind: "explored", Intensity: 1})

	hs := f.Gradient("repo://", "", 10)
	if len(hs) != 2 {
		t.Fatalf("want 2 hotspots, got %d", len(hs))
	}
	if hs[0].Resource != "repo://hot" || math.Abs(hs[0].Total-8) > 1e-9 {
		t.Fatalf("top hotspot wrong: %+v", hs[0])
	}
	if hs[0].TopSignal.Kind != "gold" {
		t.Fatalf("top signal want gold, got %s", hs[0].TopSignal.Kind)
	}
	if len(hs[0].Agents) != 2 {
		t.Fatalf("hotspot want 2 agents, got %v", hs[0].Agents)
	}

	// kind-filtered gradient: only gold counts
	gold := f.Gradient("", "gold", 10)
	if len(gold) != 1 || gold[0].Resource != "repo://hot" {
		t.Fatalf("gold gradient wrong: %+v", gold)
	}
}

func TestClaims(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	c := NewClaims()
	c.clock = clk.now

	if _, won := c.Acquire("a1", "task://42", time.Minute); !won {
		t.Fatal("first acquire must win")
	}
	if cl, won := c.Acquire("a2", "task://42", time.Minute); won {
		t.Fatal("contended acquire must lose")
	} else if cl.Agent != "a1" {
		t.Fatalf("loser must see holder a1, got %s", cl.Agent)
	}
	// holder renews
	if _, won := c.Acquire("a1", "task://42", time.Minute); !won {
		t.Fatal("holder renewal must win")
	}
	// expiry frees the lease
	clk.advance(2 * time.Minute)
	if _, won := c.Acquire("a2", "task://42", time.Minute); !won {
		t.Fatal("acquire after expiry must win")
	}
	// only the holder can release
	if c.Release("a1", "task://42") {
		t.Fatal("non-holder release must fail")
	}
	if !c.Release("a2", "task://42") {
		t.Fatal("holder release must succeed")
	}
}

func TestDanceFloorCursor(t *testing.T) {
	d := NewDanceFloor(5)
	for i := 0; i < 8; i++ {
		d.Broadcast("a1", "found", nil)
	}
	all := d.Since(0, "", 100)
	if len(all) != 5 { // ring keeps last 5
		t.Fatalf("ring want 5, got %d", len(all))
	}
	if all[0].Seq != 4 {
		t.Fatalf("oldest retained seq want 4, got %d", all[0].Seq)
	}
	after := d.Since(6, "", 100)
	if len(after) != 2 {
		t.Fatalf("cursor want 2, got %d", len(after))
	}
	d.Broadcast("a2", "other", nil)
	if got := d.Since(0, "other", 100); len(got) != 1 {
		t.Fatalf("topic filter want 1, got %d", len(got))
	}
}
