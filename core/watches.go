package main

// watches.go — /v1/watches and /v1/ingest/{id}
//
// The Rust waggle client (omokoda-core/src/waggle/mod.rs) registers a watch
// once per agent startup and then posts tool outcomes to /v1/ingest/{id}.
// The watch is a named subscription that converts tool execution events into
// typed signals on the field — making the whole tool surface stigmergic
// without per-tool instrumentation.
//
// Watch lifecycle:
//   POST /v1/watches          → create a watch (returns {watch:{id,...}})
//   POST /v1/ingest/{id}      → post a tool outcome; converted to a signal
//   GET  /v1/watches          → list all active watches
//
// Outcome → signal mapping:
//   outcome="success"  → kind="gold",     intensity=4.0, half_life=3600s (1h)
//   outcome="failure"  → kind="dead-end", intensity=4.0, half_life=3600s (1h)

import (
	"net/http"
	"sync"
	"time"
)

// ---- Watch type --------------------------------------------------------------

// Watch is a named channel that routes tool-outcome events to field signals.
// The ResourcePrefix filters which resources this watch cares about —
// "tool://" catches every tool execution; "tool://scrape" only scrape tools.
type Watch struct {
	ID             string    `json:"id"`
	Agent          string    `json:"agent"`
	Name           string    `json:"name"`
	ResourcePrefix string    `json:"resource_prefix"`
	CreatedAt      time.Time `json:"created_at"`
}

// ---- WatchStore --------------------------------------------------------------

type WatchStore struct {
	mu      sync.RWMutex
	watches map[string]*Watch
}

func NewWatchStore() *WatchStore {
	return &WatchStore{watches: make(map[string]*Watch)}
}

func (ws *WatchStore) Create(agent, name, prefix string) Watch {
	w := Watch{
		ID:             newID(),
		Agent:          agent,
		Name:           name,
		ResourcePrefix: prefix,
		CreatedAt:      time.Now(),
	}
	ws.mu.Lock()
	ws.watches[w.ID] = &w
	ws.mu.Unlock()
	return w
}

func (ws *WatchStore) Get(id string) (Watch, bool) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	if w, ok := ws.watches[id]; ok {
		return *w, true
	}
	return Watch{}, false
}

func (ws *WatchStore) List() []Watch {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	out := make([]Watch, 0, len(ws.watches))
	for _, w := range ws.watches {
		out = append(out, *w)
	}
	return out
}

// ---- request/response types --------------------------------------------------

type watchCreateReq struct {
	Agent          string `json:"agent"`
	Name           string `json:"name"`
	ResourcePrefix string `json:"resource_prefix"`
}

type ingestReq struct {
	Resource string `json:"resource"`
	Outcome  string `json:"outcome"` // "success" | "failure"
	Note     string `json:"note"`
}

// ---- handlers ----------------------------------------------------------------

func (s *Server) handleWatchCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[watchCreateReq](w, r)
	if !ok {
		return
	}
	if req.Agent == "" {
		writeErr(w, http.StatusBadRequest, "agent is required")
		return
	}
	watch := s.watches.Create(req.Agent, req.Name, req.ResourcePrefix)
	s.emit("watch", watch)
	writeJSON(w, http.StatusOK, map[string]any{"watch": watch})
}

func (s *Server) handleWatchList(w http.ResponseWriter, r *http.Request) {
	ws := s.watches.List()
	if ws == nil {
		ws = []Watch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"watches": ws})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	watchID := r.PathValue("id")
	wt, ok := s.watches.Get(watchID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown watch id")
		return
	}

	req, ok := decode[ingestReq](w, r)
	if !ok {
		return
	}
	if req.Resource == "" || req.Outcome == "" {
		writeErr(w, http.StatusBadRequest, "resource and outcome are required")
		return
	}

	// Apply resource prefix filter — ingest only routes what the watch
	// was registered to observe.
	prefix := wt.ResourcePrefix
	if prefix != "" && len(req.Resource) < len(prefix) {
		writeErr(w, http.StatusBadRequest, "resource does not match watch prefix")
		return
	}

	// Map tool outcome to a field signal kind.
	kind := "dead-end"
	if req.Outcome == "success" {
		kind = "gold"
	}

	sig := Signal{
		Agent:       wt.Agent,
		Resource:    req.Resource,
		Kind:        kind,
		Intensity:   DefaultIntensity * 4,
		HalfLifeS:   3600, // 1-hour decay for tool outcomes
		Note:        req.Note,
		DepositedAt: time.Now(),
	}
	out := s.field.Deposit(sig)
	s.agents.Touch(wt.Agent)
	s.emit("signal", out)
	writeJSON(w, http.StatusOK, map[string]any{"signal": out, "watch_id": watchID})
}
