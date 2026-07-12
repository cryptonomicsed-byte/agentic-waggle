package main

import (
	"math"
	"net/http"
	"sort"
	"sync"
	"time"
)

// AttackMetrics instruments the field for red-team scoring (Connection Map v2
// round 2, #1). It is nil in normal operation and only allocated when the
// daemon runs with -debug, so production pays nothing: the whole adversarial
// surface is a build-time-optional observability layer, never a live
// dependency.
//
// It answers three questions a red-team run needs:
//   - Sybil gold spam: are many identities depositing near-identical signals
//     on one resource in a tight window (a coordinated ring, not independent
//     corroboration)? -> suspected clusters + their inflated effective mass.
//   - Taboo griefing / flooding: which agents deposit far above the swarm
//     median rate? -> per-agent deposit rates.
//   - Lease squatting: does expiry actually reclaim within the TTL bound
//     under adversarial hold? -> reclaim-latency samples.
type AttackMetrics struct {
	mu sync.Mutex
	// per-agent deposit counts and first/last timestamps
	deposits map[string]*agentStat
	// per (resource|kind) recent deposits, for cluster detection
	recent map[string][]depositSample
	// lease acquisitions that later expired (not released): reclaim latency
	reclaimLatencies []float64
	// live leases keyed by resource -> (agent, acquiredAt, ttl)
	liveLeases map[string]leaseSample
	clock      func() time.Time
}

type agentStat struct {
	count       int
	first, last time.Time
}

type depositSample struct {
	agent     string
	intensity float64
	tier      string
	at        time.Time
}

type leaseSample struct {
	agent      string
	acquiredAt time.Time
	ttl        time.Duration
}

func NewAttackMetrics() *AttackMetrics {
	return &AttackMetrics{
		deposits:   map[string]*agentStat{},
		recent:     map[string][]depositSample{},
		liveLeases: map[string]leaseSample{},
		clock:      time.Now,
	}
}

// clusterWindow bounds how close in time deposits must be to count as a
// coordinated ring, and clusterIntensityEps how similar their intensities.
const (
	clusterWindow       = 30 * time.Second
	clusterIntensityEps = 0.5
	clusterMinAgents    = 3
	recentKeep          = 200
)

func (m *AttackMetrics) recordDeposit(sig Signal) {
	if m == nil {
		return
	}
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.deposits[sig.Agent]
	if st == nil {
		st = &agentStat{first: now}
		m.deposits[sig.Agent] = st
	}
	st.count++
	st.last = now
	key := sig.Resource + "|" + sig.Kind
	samples := append(m.recent[key], depositSample{sig.Agent, sig.Intensity, sig.EvidenceTier, now})
	// prune outside the window and cap
	cut := now.Add(-clusterWindow)
	kept := samples[:0]
	for _, s := range samples {
		if s.at.After(cut) {
			kept = append(kept, s)
		}
	}
	if len(kept) > recentKeep {
		kept = kept[len(kept)-recentKeep:]
	}
	m.recent[key] = kept
}

func (m *AttackMetrics) recordClaim(agent, resource string, ttl time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.liveLeases[resource] = leaseSample{agent, m.clock(), ttl}
	m.mu.Unlock()
}

func (m *AttackMetrics) recordRelease(resource string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.liveLeases, resource) // clean release: not a reclaim
	m.mu.Unlock()
}

// sampleReclaims moves any lease whose TTL has elapsed (holder never released
// — squatting) into the reclaim-latency record. Called on each metrics read.
func (m *AttackMetrics) sampleReclaims() {
	now := m.clock()
	for res, ls := range m.liveLeases {
		if now.Sub(ls.acquiredAt) >= ls.ttl {
			// reclaim latency past the TTL boundary: how long the squat held
			// beyond its lease before we observed the reclaim
			m.reclaimLatencies = append(m.reclaimLatencies, now.Sub(ls.acquiredAt).Seconds())
			delete(m.liveLeases, res)
		}
	}
}

// SuspectedCluster is a group of agents whose deposits on one resource look
// coordinated rather than independent.
type SuspectedCluster struct {
	Resource       string   `json:"resource"`
	Kind           string   `json:"kind"`
	Agents         []string `json:"agents"`
	MeanIntensity  float64  `json:"mean_intensity"`
	SpreadS        float64  `json:"time_spread_s"`
	InflatedWeight float64  `json:"inflated_effective"` // summed tier-weighted mass the ring fakes
}

// report scores the current state. This is what /v1/debug/attack-metrics
// returns and what a red-team run reads to grade each defense.
func (m *AttackMetrics) report() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sampleReclaims()

	// per-agent rates
	rates := make(map[string]float64, len(m.deposits))
	for agent, st := range m.deposits {
		span := st.last.Sub(st.first).Seconds()
		if span < 1 {
			span = 1
		}
		rates[agent] = float64(st.count) / span
	}

	// Sybil cluster detection: for each (resource,kind) with >= clusterMinAgents
	// distinct agents in the window and low intensity spread, flag a ring.
	var clusters []SuspectedCluster
	for key, samples := range m.recent {
		byAgent := map[string]depositSample{}
		for _, s := range samples {
			byAgent[s.agent] = s // last per agent
		}
		if len(byAgent) < clusterMinAgents {
			continue
		}
		var ints []float64
		var minT, maxT time.Time
		agents := make([]string, 0, len(byAgent))
		inflated := 0.0
		for a, s := range byAgent {
			agents = append(agents, a)
			ints = append(ints, s.intensity)
			inflated += s.intensity * TierWeight(s.tier)
			if minT.IsZero() || s.at.Before(minT) {
				minT = s.at
			}
			if s.at.After(maxT) {
				maxT = s.at
			}
		}
		mean, spread := meanStdev(ints)
		if spread <= clusterIntensityEps {
			res, kind := splitKey(key)
			sort.Strings(agents)
			clusters = append(clusters, SuspectedCluster{
				Resource: res, Kind: kind, Agents: agents,
				MeanIntensity: mean, SpreadS: maxT.Sub(minT).Seconds(),
				InflatedWeight: inflated,
			})
		}
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].InflatedWeight > clusters[j].InflatedWeight })

	// reclaim latency stats
	var reclaimMax, reclaimMean float64
	for _, l := range m.reclaimLatencies {
		reclaimMean += l
		if l > reclaimMax {
			reclaimMax = l
		}
	}
	if len(m.reclaimLatencies) > 0 {
		reclaimMean /= float64(len(m.reclaimLatencies))
	}

	medianRate := median(rates)
	var floodAgents []map[string]any
	for agent, r := range rates {
		if medianRate > 0 && r > 5*medianRate {
			floodAgents = append(floodAgents, map[string]any{"agent": agent, "rate_per_s": r})
		}
	}

	return map[string]any{
		"deposit_rates_per_s": rates,
		"median_rate_per_s":   medianRate,
		"flooding_agents":     floodAgents,
		"suspected_clusters":  clusters,
		"cluster_count":       len(clusters),
		"lease_reclaims":      len(m.reclaimLatencies),
		"reclaim_latency_s":   map[string]float64{"max": reclaimMax, "mean": reclaimMean},
		"live_leases":         len(m.liveLeases),
	}
}

func (s *Server) handleAttackMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		writeErr(w, http.StatusNotFound, "attack metrics disabled; start waggled with -debug")
		return
	}
	writeJSON(w, http.StatusOK, s.metrics.report())
}

// ---- small stats helpers ----

func meanStdev(xs []float64) (mean, stdev float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		stdev += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(stdev / float64(len(xs)))
}

func median(m map[string]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	xs := make([]float64, 0, len(m))
	for _, v := range m {
		xs = append(xs, v)
	}
	sort.Float64s(xs)
	return xs[len(xs)/2]
}

func splitKey(key string) (resource, kind string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '|' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
