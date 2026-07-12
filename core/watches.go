package main

import (
	"sort"
	"sync"
	"time"

	"github.com/cryptonomicsed-byte/agentic/core/kernel"
)

// Watch is a registered derivation rule: instead of asking every system to
// call mark, a watch turns state transitions pushed to its ingest endpoint
// into deposits. An ỌṢỌVM compile log, a CI webhook, a Sui event relay — each
// POSTS its transitions and the watch derives the signal. Deposits derived
// this way carry evidence_tier "watch-derived" by default: the number came
// from the instrument observing the state change, not from the interested
// party's self-report.
type Watch struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Name  string `json:"name,omitempty"`
	// ResourcePrefix, when set, is prepended to incoming resources so an
	// ingest source can speak relative paths.
	ResourcePrefix string `json:"resource_prefix,omitempty"`
	// Map translates an incoming outcome word into a signal kind. Defaults:
	// success->gold, failure->dead-end. An event may also name its kind
	// directly and skip the map.
	Map       map[string]string `json:"map,omitempty"`
	Tier      string            `json:"evidence_tier,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Ingested  int               `json:"ingested"`
}

type Watches struct {
	mu   sync.RWMutex
	byID map[string]*Watch
}

func NewWatches() *Watches {
	return &Watches{byID: make(map[string]*Watch)}
}

func defaultWatchMap() map[string]string {
	return map[string]string{"success": "gold", "failure": "dead-end"}
}

// Register stores a watch. Idempotent by ID so journal replay restores
// watches exactly.
func (ws *Watches) Register(w Watch) Watch {
	if w.ID == "" {
		w.ID = newID()
	}
	if len(w.Map) == 0 {
		w.Map = defaultWatchMap()
	}
	if !kernel.IsTier(w.Tier) {
		w.Tier = "watch-derived"
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now()
	}
	ws.mu.Lock()
	cp := w
	ws.byID[w.ID] = &cp
	ws.mu.Unlock()
	return w
}

func (ws *Watches) Get(id string) (Watch, bool) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	if w, ok := ws.byID[id]; ok {
		return *w, true
	}
	return Watch{}, false
}

func (ws *Watches) List() []Watch {
	ws.mu.RLock()
	out := make([]Watch, 0, len(ws.byID))
	for _, w := range ws.byID {
		out = append(out, *w)
	}
	ws.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (ws *Watches) touch(id string) {
	ws.mu.Lock()
	if w, ok := ws.byID[id]; ok {
		w.Ingested++
	}
	ws.mu.Unlock()
}

// WatchEvent is one state transition pushed to a watch's ingest endpoint.
type WatchEvent struct {
	Resource  string            `json:"resource"`
	Outcome   string            `json:"outcome,omitempty"` // translated via the watch's map
	Kind      string            `json:"kind,omitempty"`    // or named directly
	Subtype   string            `json:"subtype,omitempty"`
	Intensity float64           `json:"intensity,omitempty"`
	HalfLifeS float64           `json:"half_life_s,omitempty"`
	Note      string            `json:"note,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Derive turns an event into the signal the watch deposits, or ok=false if
// the event names neither a kind nor a mapped outcome.
func (w *Watch) Derive(ev WatchEvent) (Signal, bool) {
	kind := ev.Kind
	if kind == "" {
		kind = w.Map[ev.Outcome]
	}
	if kind == "" || ev.Resource == "" {
		return Signal{}, false
	}
	return Signal{
		Agent:        w.Agent,
		Resource:     w.ResourcePrefix + ev.Resource,
		Kind:         kind,
		Subtype:      ev.Subtype,
		Intensity:    ev.Intensity,
		HalfLifeS:    ev.HalfLifeS,
		EvidenceTier: w.Tier,
		Note:         ev.Note,
		Meta:         ev.Meta,
	}, true
}
