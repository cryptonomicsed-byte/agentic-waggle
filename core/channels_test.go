package main

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBoundedChannelDefaults(t *testing.T) {
	f, clk := newTestField()

	// stability 0.8 verdict: intensity 8, everything else from the channel
	out := f.Deposit(Signal{Agent: "oracle", Resource: "loom://strategy/sniper", Kind: "bounded", Intensity: 8})
	if out.HalfLifeS != 7200 {
		t.Fatalf("bounded default half-life want 7200, got %v", out.HalfLifeS)
	}
	if out.Decay != "power" {
		t.Fatalf("bounded default kernel want power, got %q", out.Decay)
	}
	// alpha(s) = 2.0 - 1.5*I/10 = 2.0 - 1.5*0.8 = 0.8
	if math.Abs(out.Alpha-0.8) > 1e-9 {
		t.Fatalf("bounded alpha want 0.8, got %v", out.Alpha)
	}

	// still halves at exactly one half-life despite the tuned alpha
	clk.advance(7200 * time.Second)
	sigs := f.Sniff(SniffQuery{Resource: "loom://strategy/sniper"})
	if got := sigs[0].Intensity; math.Abs(got-4) > 1e-9 {
		t.Fatalf("bounded at one half-life want 4, got %v", got)
	}
}

func TestBoundedReplaceReinforcement(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "oracle", Resource: "r", Kind: "bounded", Intensity: 3})
	out := f.Deposit(Signal{Agent: "oracle", Resource: "r", Kind: "bounded", Intensity: 3})
	// two s=0.3 scans must not read as s=0.6
	if math.Abs(out.Intensity-3) > 1e-9 {
		t.Fatalf("replace-mode reinforcement want 3, got %v", out.Intensity)
	}
	// a re-measurement sets the value in either direction
	out = f.Deposit(Signal{Agent: "oracle", Resource: "r", Kind: "bounded", Intensity: 9})
	if math.Abs(out.Intensity-9) > 1e-9 {
		t.Fatalf("replace-mode update want 9, got %v", out.Intensity)
	}
	// gold keeps adding
	f.Deposit(Signal{Agent: "a1", Resource: "r", Kind: "gold", Intensity: 2})
	gold := f.Deposit(Signal{Agent: "a1", Resource: "r", Kind: "gold", Intensity: 2})
	if math.Abs(gold.Intensity-4) > 1e-9 {
		t.Fatalf("gold additive reinforcement want 4, got %v", gold.Intensity)
	}
}

func TestEvidenceTierWeighting(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "r", Kind: "gold", Intensity: 5}) // defaults to self-report
	f.Deposit(Signal{Agent: "a2", Resource: "r", Kind: "gold", Intensity: 5, EvidenceTier: "on-chain-anchored"})
	f.Deposit(Signal{Agent: "a3", Resource: "r", Kind: "gold", Intensity: 5, EvidenceTier: "not-a-tier"})

	sigs := f.Sniff(SniffQuery{Resource: "r"})
	for _, s := range sigs {
		want := 5 * TierWeight(s.EvidenceTier)
		if math.Abs(s.Effective-want) > 1e-9 {
			t.Fatalf("%s effective want %v, got %v", s.Agent, want, s.Effective)
		}
	}
	// bogus tier canonicalized on deposit
	for _, s := range sigs {
		if s.Agent == "a3" && s.EvidenceTier != "self-report" {
			t.Fatalf("unknown tier must canonicalize to self-report, got %q", s.EvidenceTier)
		}
	}

	// min_tier filters out the self-reports
	corr := f.Sniff(SniffQuery{Resource: "r", MinTier: "corroborated"})
	if len(corr) != 1 || corr[0].Agent != "a2" {
		t.Fatalf("min_tier filter want only a2, got %+v", corr)
	}

	// weighted gradient uses effective sums: 5*0.2 + 5*1.0 + 5*0.2 = 7
	hs := f.GradientOpts("", "", 10, -1, true, false)
	if len(hs) != 1 || math.Abs(hs[0].Total-7) > 1e-9 {
		t.Fatalf("weighted gradient want 7, got %+v", hs)
	}
}

func TestCrossInhibition(t *testing.T) {
	f, _ := newTestField()
	// gold on a resource the oracle called a deep escape zone (stability 0.1)
	f.Deposit(Signal{Agent: "trader", Resource: "loom://s/p1", Kind: "gold", Intensity: 8, EvidenceTier: "on-chain-anchored"})
	f.Deposit(Signal{Agent: "oracle", Resource: "loom://s/p1", Kind: "bounded", Intensity: 1})

	sigs := f.Sniff(SniffQuery{Resource: "loom://s/p1", Kind: "gold"})
	// m = max(0.25, min(1, (1/10)/0.5)) = 0.25 → effective = 8 * 1.0 * 0.25 = 2
	if got := sigs[0].Effective; math.Abs(got-2) > 1e-9 {
		t.Fatalf("dead-cat-bounce suppression want 2, got %v", got)
	}

	// gold on a robust island (stability 0.9): no suppression
	f.Deposit(Signal{Agent: "trader", Resource: "loom://s/p2", Kind: "gold", Intensity: 8, EvidenceTier: "on-chain-anchored"})
	f.Deposit(Signal{Agent: "oracle", Resource: "loom://s/p2", Kind: "bounded", Intensity: 9})
	sigs = f.Sniff(SniffQuery{Resource: "loom://s/p2", Kind: "gold"})
	if got := sigs[0].Effective; math.Abs(got-8) > 1e-9 {
		t.Fatalf("island gold must be unsuppressed, want 8, got %v", got)
	}

	// taboo suppresses in "high" mode: intensity 9 → m = max(0.1, 1-0.9) = 0.1
	f.Deposit(Signal{Agent: "trader", Resource: "loom://s/p3", Kind: "gold", Intensity: 8, EvidenceTier: "on-chain-anchored"})
	f.Deposit(Signal{Agent: "obatala", Resource: "loom://s/p3", Kind: "taboo", Intensity: 9})
	sigs = f.Sniff(SniffQuery{Resource: "loom://s/p3", Kind: "gold"})
	if got := sigs[0].Effective; math.Abs(got-0.8) > 1e-6 {
		t.Fatalf("taboo suppression want 0.8, got %v", got)
	}
}

func TestExplain(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "trader", Resource: "loom://s/p1", Kind: "gold", Intensity: 8, EvidenceTier: "watch-derived"})
	f.Deposit(Signal{Agent: "oracle", Resource: "loom://s/p1", Kind: "bounded", Intensity: 1})
	f.Deposit(Signal{Agent: "x", Resource: "loom://s/p2", Kind: "explored", Intensity: 4})

	ex := f.Explain("loom://s/p1")
	if len(ex.Contributions) != 2 {
		t.Fatalf("want 2 contributions, got %d", len(ex.Contributions))
	}
	var gold *Contribution
	for i := range ex.Contributions {
		if ex.Contributions[i].Signal.Kind == "gold" {
			gold = &ex.Contributions[i]
		}
	}
	if gold == nil {
		t.Fatal("gold contribution missing")
	}
	if gold.TierWeight != 0.6 {
		t.Fatalf("gold tier weight want 0.6, got %v", gold.TierWeight)
	}
	if len(gold.Inhibitions) != 1 || gold.Inhibitions[0].SourceKind != "bounded" {
		t.Fatalf("gold must show the bounded inhibition, got %+v", gold.Inhibitions)
	}
	// effective = 8 * 0.6 * 0.25 = 1.2
	if math.Abs(gold.Effective-1.2) > 1e-9 {
		t.Fatalf("gold effective want 1.2, got %v", gold.Effective)
	}
	// sibling p2 contributes diffusion: 0.05 * 4 = 0.2
	if math.Abs(ex.Diffusion-0.2) > 1e-9 {
		t.Fatalf("diffusion want 0.2, got %v", ex.Diffusion)
	}
}

func TestBatchRollup(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "a1", Resource: "repo://src/auth/token.go", Kind: "gold", Intensity: 5})
	f.Deposit(Signal{Agent: "a2", Resource: "repo://src/auth/session.go", Kind: "explored", Intensity: 3})
	f.Deposit(Signal{Agent: "a1", Resource: "repo://src/db/conn.go", Kind: "explored", Intensity: 1})

	res := f.BatchRollup([]string{"repo://src/auth", "repo://src/db", "repo://docs"}, "", false)
	if math.Abs(res["repo://src/auth"].Total-8) > 1e-9 {
		t.Fatalf("auth rollup want 8, got %+v", res["repo://src/auth"])
	}
	if res["repo://src/auth"].Resources != 2 {
		t.Fatalf("auth rollup want 2 resources, got %d", res["repo://src/auth"].Resources)
	}
	if math.Abs(res["repo://src/db"].Total-1) > 1e-9 {
		t.Fatalf("db rollup want 1, got %+v", res["repo://src/db"])
	}
	if res["repo://docs"].Total != 0 {
		t.Fatalf("cold branch must read 0, got %+v", res["repo://docs"])
	}
}

func TestTerritoryTempoAndVelocity(t *testing.T) {
	ts := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts.Close()

	// slow territory: tempo 4 quadruples the default half-life
	code, _ := doJSON(t, ts, "POST", "/v1/territories", map[string]any{"prefix": "ethics://", "tempo": 4})
	if code != 200 {
		t.Fatalf("territory set: %d", code)
	}
	code, sig := doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "obatala", "resource": "ethics://cases/1", "kind": "taboo"})
	if code != 200 {
		t.Fatalf("deposit: %d", code)
	}
	// taboo channel default 86400 * tempo 4
	if got := sig["half_life_s"].(float64); math.Abs(got-345600) > 1e-6 {
		t.Fatalf("tempo-scaled half-life want 345600, got %v", got)
	}

	// an explicit half-life is always honored
	code, sig = doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "obatala", "resource": "ethics://cases/2", "kind": "taboo", "half_life_s": 60})
	if code != 200 || sig["half_life_s"].(float64) != 60 {
		t.Fatalf("explicit half-life overridden: %v", sig["half_life_s"])
	}

	// claim velocity shortens defaults, but ONLY inside a registered
	// territory: dynamic evaporation is opt-in so unregistered field stays
	// deterministic run after run
	for i := 0; i < 10; i++ {
		doJSON(t, ts, "POST", "/v1/claims", map[string]any{"agent": "a1", "resource": "task://hot/" + strings.Repeat("x", i+1), "ttl_s": 60})
	}
	code, sig = doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "a1", "resource": "task://hot/x", "kind": "explored"})
	if code != 200 || sig["half_life_s"].(float64) != 1800 {
		t.Fatalf("unregistered territory must keep classic defaults, got %v", sig["half_life_s"])
	}
	doJSON(t, ts, "POST", "/v1/territories", map[string]any{"prefix": "task://", "tempo": 1})
	code, sig = doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "a1", "resource": "task://hot/x2", "kind": "explored"})
	if code != 200 {
		t.Fatalf("deposit: %d", code)
	}
	if got := sig["half_life_s"].(float64); math.Abs(got-1200) > 1e-6 { // 1800 / 1.5 (10 claims → speedup 1.5)
		t.Fatalf("velocity-shortened half-life want 1200, got %v", got)
	}
}

func TestWatchIngest(t *testing.T) {
	ts := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts.Close()

	code, reg := doJSON(t, ts, "POST", "/v1/watches", map[string]any{
		"agent": "osovm-compiler", "name": "compile log", "resource_prefix": "osovm://build/"})
	if code != 200 {
		t.Fatalf("watch register: %d %v", code, reg)
	}
	ingest := reg["ingest_path"].(string)

	// mapped outcome → dead-end at watch-derived tier
	code, sig := doJSON(t, ts, "POST", ingest, map[string]any{
		"resource": "ritual/summon.osovm", "outcome": "failure", "subtype": "type-checking", "note": "unbound veil"})
	if code != 200 {
		t.Fatalf("ingest: %d %v", code, sig)
	}
	if sig["kind"] != "dead-end" || sig["evidence_tier"] != "watch-derived" {
		t.Fatalf("derived signal wrong: %v", sig)
	}
	if sig["resource"] != "osovm://build/ritual/summon.osovm" {
		t.Fatalf("prefix not applied: %v", sig["resource"])
	}
	if sig["subtype"] != "type-checking" {
		t.Fatalf("subtype lost: %v", sig)
	}

	// direct kind bypasses the map
	code, sig = doJSON(t, ts, "POST", ingest, map[string]any{"resource": "r2", "kind": "bounded", "intensity": 8})
	if code != 200 || sig["kind"] != "bounded" {
		t.Fatalf("direct kind ingest: %d %v", code, sig)
	}

	// unknown watch 404s; unmappable event 400s
	if code, _ := doJSON(t, ts, "POST", "/v1/ingest/nope", map[string]any{"resource": "r"}); code != 404 {
		t.Fatalf("unknown watch want 404, got %d", code)
	}
	if code, _ := doJSON(t, ts, "POST", ingest, map[string]any{"resource": "r", "outcome": "shrug"}); code != 400 {
		t.Fatalf("unmapped outcome want 400, got %d", code)
	}
}

func TestChannelRegistrationAndManifest(t *testing.T) {
	ts := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts.Close()

	code, ch := doJSON(t, ts, "POST", "/v1/channels", map[string]any{
		"name": "resonance", "doc": "Ọ̀ṣun's semantic resonance", "default_half_life_s": 3600,
		"decay_kernel": "power", "default_alpha": 1.5,
		"cross_inhibits": []map[string]any{{"channel": "dead-end", "mode": "bogus"}}})
	if code != 200 {
		t.Fatalf("channel register: %d %v", code, ch)
	}
	// bogus inhibition dropped, not stored
	if _, has := ch["cross_inhibits"]; has {
		t.Fatalf("bogus inhibition must be dropped: %v", ch)
	}

	code, list := doJSON(t, ts, "GET", "/v1/channels", nil)
	if code != 200 {
		t.Fatalf("channels list: %d", code)
	}
	names := map[string]bool{}
	for _, c := range list["channels"].([]any) {
		names[c.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"bounded", "taboo", "gold", "resonance", "federation-health"} {
		if !names[want] {
			t.Fatalf("channel %q missing from list", want)
		}
	}
	if len(list["evidence_tiers"].([]any)) != 5 {
		t.Fatalf("evidence tier ladder wrong: %v", list["evidence_tiers"])
	}

	// the registered channel drives deposits immediately
	code, sig := doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "osun", "resource": "r", "kind": "resonance"})
	if code != 200 || sig["half_life_s"].(float64) != 3600 || sig["decay"] != "power" {
		t.Fatalf("registered channel defaults not applied: %v", sig)
	}

	// manifest exposes the channels block
	code, man := doJSON(t, ts, "GET", "/.well-known/waggle.json", nil)
	if code != 200 || man["channels"] == nil || man["evidence_tiers"] == nil {
		t.Fatalf("manifest missing channels block: %d", code)
	}
}

func TestCostEfficiencyRanking(t *testing.T) {
	f, _ := newTestField()
	// two equally strong golds: one found cheaply, one after heavy reasoning
	f.Deposit(Signal{Agent: "cheap", Resource: "repo://a", Kind: "gold", Intensity: 8,
		Cost: &Cost{Tokens: 100}})
	f.Deposit(Signal{Agent: "pricey", Resource: "repo://b", Kind: "gold", Intensity: 8,
		Cost: &Cost{Tokens: 50000}})
	// a costless gold ranks as maximally efficient (epsilon floor, no div-by-0)
	f.Deposit(Signal{Agent: "free", Resource: "repo://c", Kind: "gold", Intensity: 6})

	// default ranking is by intensity: the two 8s lead, order among them
	// unspecified, free (6) last
	byIntensity := f.Sniff(SniffQuery{Prefix: "repo://"})
	if byIntensity[len(byIntensity)-1].Agent != "free" {
		t.Fatalf("intensity ranking should put the weaker signal last, got %v", byIntensity[len(byIntensity)-1].Agent)
	}

	// cost-efficiency ranking: free > cheap > pricey
	byCost := f.Sniff(SniffQuery{Prefix: "repo://", Optimize: "cost_efficiency"})
	order := []string{byCost[0].Agent, byCost[1].Agent, byCost[2].Agent}
	if order[0] != "free" || order[1] != "cheap" || order[2] != "pricey" {
		t.Fatalf("cost-efficiency ranking wrong: %v", order)
	}
	if byCost[0].CostEfficiency <= byCost[2].CostEfficiency {
		t.Fatalf("free must be more cost-efficient than pricey: %v vs %v", byCost[0].CostEfficiency, byCost[2].CostEfficiency)
	}
}

func TestCostAccumulatesOnReinforcement(t *testing.T) {
	f, _ := newTestField()
	f.Deposit(Signal{Agent: "a", Resource: "r", Kind: "gold", Intensity: 3, Cost: &Cost{Tokens: 100, Dollars: 0.01}})
	out := f.Deposit(Signal{Agent: "a", Resource: "r", Kind: "gold", Intensity: 3, Cost: &Cost{Tokens: 200, Dollars: 0.02}})
	if out.Cost == nil || out.Cost.Tokens != 300 || math.Abs(out.Cost.Dollars-0.03) > 1e-9 {
		t.Fatalf("reinforcement should sum cost, got %+v", out.Cost)
	}
	// bounded (replace mode) supersedes cost instead of summing
	f.Deposit(Signal{Agent: "o", Resource: "r2", Kind: "bounded", Intensity: 5, Cost: &Cost{Tokens: 1000}})
	b := f.Deposit(Signal{Agent: "o", Resource: "r2", Kind: "bounded", Intensity: 5, Cost: &Cost{Tokens: 40}})
	if b.Cost == nil || b.Cost.Tokens != 40 {
		t.Fatalf("replace-mode cost should supersede, got %+v", b.Cost)
	}
}

func TestRecall(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, ServerConfig{})
	if err := srv.replay(dir); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)

	doJSON(t, ts, "POST", "/v1/signals", map[string]any{
		"agent": "oracle", "resource": "loom://s/p1", "kind": "bounded", "intensity": 8, "half_life_s": 3600})
	mid := time.Now()
	time.Sleep(10 * time.Millisecond)
	// regression: the re-measurement drops the verdict
	doJSON(t, ts, "POST", "/v1/signals", map[string]any{
		"agent": "oracle", "resource": "loom://s/p1", "kind": "bounded", "intensity": 3, "half_life_s": 3600})

	// live field sees the current (replaced) verdict
	_, sniff := doJSON(t, ts, "GET", "/v1/sniff?resource=loom://s/p1", nil)
	if got := sniff["signals"].([]any)[0].(map[string]any)["intensity"].(float64); got > 3.01 {
		t.Fatalf("live verdict want ~3, got %v", got)
	}

	// recall at mid sees the historical verdict — the regression detector's raw material
	code, rec := doJSON(t, ts, "GET", "/v1/recall?resource=loom://s/p1&at="+mid.Format(time.RFC3339Nano), nil)
	if code != 200 {
		t.Fatalf("recall: %d %v", code, rec)
	}
	sigs := rec["signals"].([]any)
	if len(sigs) != 1 {
		t.Fatalf("recall want 1 signal, got %v", rec)
	}
	if got := sigs[0].(map[string]any)["intensity"].(float64); math.Abs(got-8) > 0.1 {
		t.Fatalf("recalled intensity want ~8, got %v", got)
	}
	ts.Close()
	store.Close()

	// no journal → 409
	ts2 := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts2.Close()
	if code, _ := doJSON(t, ts2, "GET", "/v1/recall?resource=r&at="+time.Now().Format(time.RFC3339), nil); code != 409 {
		t.Fatalf("recall without journal want 409, got %d", code)
	}
}

func TestBatchSniffEndpoint(t *testing.T) {
	ts := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts.Close()

	doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "a1", "resource": "repo://src/a", "kind": "gold", "intensity": 5})
	doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "a1", "resource": "repo://src/b", "kind": "explored", "intensity": 2})

	code, res := doJSON(t, ts, "POST", "/v1/sniff/batch", map[string]any{"uris": []string{"repo://src/a", "repo://src/b", "repo://src/c"}})
	if code != 200 {
		t.Fatalf("batch: %d %v", code, res)
	}
	results := res["results"].(map[string]any)
	if got := results["repo://src/a"].(map[string]any)["total"].(float64); math.Abs(got-5) > 0.01 {
		t.Fatalf("batch a want 5, got %v", got)
	}
	if got := results["repo://src/c"].(map[string]any)["total"].(float64); got != 0 {
		t.Fatalf("batch c want 0, got %v", got)
	}
	if code, _ := doJSON(t, ts, "POST", "/v1/sniff/batch", map[string]any{"uris": []string{}}); code != 400 {
		t.Fatalf("empty batch want 400, got %d", code)
	}
}

func TestExplainEndpointAndReplayOfNewTypes(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, ServerConfig{})
	if err := srv.replay(dir); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)

	doJSON(t, ts, "POST", "/v1/channels", map[string]any{"name": "resonance", "default_half_life_s": 3600})
	code, reg := doJSON(t, ts, "POST", "/v1/watches", map[string]any{"agent": "ogun"})
	if code != 200 {
		t.Fatalf("watch register: %d", code)
	}
	watchID := reg["watch"].(map[string]any)["id"].(string)
	doJSON(t, ts, "POST", "/v1/territories", map[string]any{"prefix": "loom://", "tempo": 0.5})
	doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "a1", "resource": "r", "kind": "gold", "intensity": 5})

	code, ex := doJSON(t, ts, "GET", "/v1/explain?resource=r", nil)
	if code != 200 || len(ex["contributions"].([]any)) != 1 {
		t.Fatalf("explain: %d %v", code, ex)
	}
	if code, _ := doJSON(t, ts, "GET", "/v1/explain", nil); code != 400 {
		t.Fatalf("explain without resource want 400, got %d", code)
	}
	ts.Close()
	store.Close()

	// restart: channels, watches and territories survive the journal
	srv2 := NewServer(nil, ServerConfig{})
	if err := srv2.replay(dir); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, ok := srv2.field.channels.Get("resonance"); !ok {
		t.Fatal("registered channel lost across restart")
	}
	if _, ok := srv2.watches.Get(watchID); !ok {
		t.Fatal("watch lost across restart")
	}
	if got, covered := srv2.territories.Tempo("loom://x"); !covered || got != 0.5 {
		t.Fatalf("territory tempo lost across restart: %v (covered=%v)", got, covered)
	}
}
