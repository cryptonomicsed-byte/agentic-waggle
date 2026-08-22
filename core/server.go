package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

// Server wires the substrate's parts behind the HTTP API. Every capability is
// exposed as a plain JSON-over-HTTP action and described in the .well-known
// manifest, so any agent that can make an HTTP request can discover and use
// the whole surface without human-written glue.
type Server struct {
	field       *Field
	agents      *Registry
	claims      *Claims
	floor       *DanceFloor
	memory      *Memory
	watches     *Watches
	territories *Territories
	hub         *Hub
	store       *Store
	mux         *http.ServeMux
	start       time.Time
	dataDir     string         // journal directory; recall needs it ("" = no persistence)
	metrics     *AttackMetrics // non-nil only under -debug; red-team scoring
	tabooAuth   *TabooAuth     // non-nil when Èṣù's taboo-capability key is configured
}

// EnableDebug turns on the attack-metrics instrumentation and its endpoint.
// Off by default so production carries no adversarial-observability overhead.
func (s *Server) EnableDebug() {
	s.metrics = NewAttackMetrics()
	s.mux.HandleFunc("GET /v1/debug/attack-metrics", s.handleAttackMetrics)
}

// EnableTabooAuth configures verification of Èṣù taboo-capability tokens against
// the given hex ed25519 public key. With enforce, unauthenticated taboo deposits
// are refused; without it they are accepted but flagged taboo_authenticated=false
// so a transition can be observed before it is enforced.
func (s *Server) EnableTabooAuth(pubHex string, enforce bool) error {
	ta, err := NewTabooAuth(pubHex, enforce)
	if err != nil {
		return err
	}
	s.tabooAuth = ta
	return nil
}

func NewServer(store *Store) *Server {
	s := &Server{
		field:       NewField(),
		agents:      NewRegistry(),
		claims:      NewClaims(),
		floor:       NewDanceFloor(1000),
		memory:      NewMemory(),
		watches:     NewWatches(),
		territories: NewTerritories(),
		hub:         NewHub(),
		store:       store,
		mux:         http.NewServeMux(),
		start:       time.Now(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /.well-known/waggle.json", s.handleManifest)
	m.HandleFunc("GET /v1/status", s.handleStatus)

	m.HandleFunc("POST /v1/agents", s.handleRegister)
	m.HandleFunc("GET /v1/agents", s.handleAgents)
	m.HandleFunc("GET /v1/agents/{id}", s.handleAgent)

	m.HandleFunc("POST /v1/signals", s.handleDeposit)
	m.HandleFunc("GET /v1/sniff", s.handleSniff)
	m.HandleFunc("POST /v1/sniff/batch", s.handleSniffBatch)
	m.HandleFunc("GET /v1/gradient", s.handleGradient)
	m.HandleFunc("GET /v1/explain", s.handleExplain)
	m.HandleFunc("GET /v1/recall", s.handleRecall)
	m.HandleFunc("GET /v1/recall/window", s.handleRecallWindow)

	m.HandleFunc("GET /v1/channels", s.handleChannels)
	m.HandleFunc("POST /v1/channels", s.handleChannelRegister)

	m.HandleFunc("POST /v1/watches", s.handleWatchRegister)
	m.HandleFunc("GET /v1/watches", s.handleWatches)
	m.HandleFunc("POST /v1/ingest/{id}", s.handleIngest)

	m.HandleFunc("POST /v1/territories", s.handleTerritorySet)
	m.HandleFunc("GET /v1/territories", s.handleTerritories)

	m.HandleFunc("GET /v1/snapshot", s.handleSnapshotExport)
	m.HandleFunc("POST /v1/snapshot/load", s.handleSnapshotLoad)

	m.HandleFunc("POST /v1/claims", s.handleClaim)
	m.HandleFunc("POST /v1/claims/release", s.handleRelease)
	m.HandleFunc("GET /v1/claims", s.handleClaims)

	m.HandleFunc("POST /v1/dances", s.handleDance)
	m.HandleFunc("GET /v1/dances", s.handleDances)

	m.HandleFunc("GET /v1/memory/{ns...}", s.handleMemoryGet)
	m.HandleFunc("PUT /v1/memory/{ns...}", s.handleMemoryPut)
	m.HandleFunc("DELETE /v1/memory/{ns...}", s.handleMemoryDelete)

	m.HandleFunc("GET /v1/events", s.handleEvents)
	m.HandleFunc("GET /", s.handleObservatory)
}

// ---- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return v, false
	}
	return v, true
}

// emit publishes to the live stream and journals the mutation.
func (s *Server) emit(typ string, payload any) {
	s.hub.Publish(typ, payload)
	s.store.Append(typ, payload)
}

// ---- agents -------------------------------------------------------------------

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	p, ok := decode[Profile](w, r)
	if !ok {
		return
	}
	out := s.agents.Register(p)
	s.emit("agent", out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.agents.List()})
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.agents.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown agent")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ---- signals ------------------------------------------------------------------

// depositReq is a Signal plus the out-of-band capability token that authorizes
// it. The token is never stored on the signal — only its verified verdict is,
// in TabooAuthenticated — so it is not journaled or served back.
type depositReq struct {
	Signal
	Capability string `json:"capability,omitempty"`
}

func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[depositReq](w, r)
	if !ok {
		return
	}
	sig := req.Signal
	if sig.Agent == "" || sig.Resource == "" || sig.Kind == "" {
		writeErr(w, http.StatusBadRequest, "agent, resource and kind are required")
		return
	}
	// Èṣù's gate: taboo censors an action, so it must be authenticatable. When a
	// key is configured, a taboo deposit's capability is verified; enforce mode
	// refuses an unauthenticated one, transition mode records the verdict.
	if sig.Kind == "taboo" && s.tabooAuth != nil {
		authed := s.tabooAuth.verify(sig.Agent, req.Capability, time.Now())
		if s.tabooAuth.enforce && !authed {
			writeErr(w, http.StatusForbidden,
				"taboo deposits require a valid issuer capability token (scope=taboo); see manifest action deposit param 'capability'")
			return
		}
		sig.TabooAuthenticated = &authed
	}
	s.applyRhythm(&sig)
	out := s.field.Deposit(sig)
	s.agents.Touch(sig.Agent)
	s.metrics.recordDeposit(out)
	s.emit("signal", out)
	writeJSON(w, http.StatusOK, out)
}

// applyRhythm resolves the default half-life for a deposit that omitted one,
// folding in the territory tempo (Ọya's heartbeat) and — inside registered
// territories only — the claim-velocity evaporation multiplier: contested
// territory decays up to twice as fast. Unregistered field keeps the classic
// deterministic defaults, so swarms that never opt in behave identically run
// after run. The adjustment happens before the deposit is journaled, so
// replayed decay is a pure function of the stored record.
func (s *Server) applyRhythm(sig *Signal) {
	if sig.HalfLifeS > 0 {
		return // an explicit half-life is always honored
	}
	tempo, covered := s.territories.Tempo(sig.Resource)
	if !covered {
		return // untouched: let the field apply its own defaults
	}
	base := float64(DefaultHalfLifeS)
	if ch, ok := s.field.channelOf(sig.Kind); ok && ch.DefaultHalfLifeS > 0 {
		base = ch.DefaultHalfLifeS
	}
	velocity := s.claims.Velocity(prefixAt(sig.Resource, 1))
	if velocity > 20 {
		velocity = 20
	}
	speedup := 1 + float64(velocity)/20 // 20 claims in 10 min halves the half-life
	sig.HalfLifeS = base * tempo / speedup
}

func (s *Server) handleSniff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	min, _ := strconv.ParseFloat(q.Get("min"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	sigs := s.field.Sniff(SniffQuery{
		Resource: q.Get("resource"),
		Prefix:   q.Get("prefix"),
		Kind:     q.Get("kind"),
		Agent:    q.Get("agent"),
		Min:      min,
		MinTier:  q.Get("min_tier"),
		Optimize: q.Get("optimize"),
		Limit:    limit,
	})
	if sigs == nil {
		sigs = []Signal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"signals": sigs})
}

type batchSniffReq struct {
	URIs     []string `json:"uris"`
	Kind     string   `json:"kind,omitempty"`
	Weighted bool     `json:"weighted,omitempty"`
}

func (s *Server) handleSniffBatch(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[batchSniffReq](w, r)
	if !ok {
		return
	}
	if len(req.URIs) == 0 {
		writeErr(w, http.StatusBadRequest, "uris is required")
		return
	}
	if len(req.URIs) > 256 {
		writeErr(w, http.StatusBadRequest, "at most 256 uris per batch")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": s.field.BatchRollup(req.URIs, req.Kind, req.Weighted),
	})
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	resource := r.URL.Query().Get("resource")
	if resource == "" {
		writeErr(w, http.StatusBadRequest, "resource is required")
		return
	}
	ex := s.field.Explain(resource)
	if ex.Contributions == nil {
		ex.Contributions = []Contribution{}
	}
	writeJSON(w, http.StatusOK, ex)
}

func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	atStr := q.Get("at")
	if atStr == "" {
		writeErr(w, http.StatusBadRequest, "at is required (RFC3339 timestamp)")
		return
	}
	at, err := time.Parse(time.RFC3339, atStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "at must be RFC3339: "+err.Error())
		return
	}
	min, _ := strconv.ParseFloat(q.Get("min"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	sigs, err := RecallAt(s.dataDir, at, SniffQuery{
		Resource: q.Get("resource"),
		Prefix:   q.Get("prefix"),
		Kind:     q.Get("kind"),
		Agent:    q.Get("agent"),
		Min:      min,
		Limit:    limit,
	})
	if err == ErrNoJournal {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sigs == nil {
		sigs = []Signal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"at": at, "signals": sigs})
}

func (s *Server) handleRecallWindow(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	until := time.Now()
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "until must be RFC3339: "+err.Error())
			return
		}
		until = t
	}
	since := until.Add(-24 * time.Hour) // default: a rolling day
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since must be RFC3339: "+err.Error())
			return
		}
		since = t
	}
	events, err := RecallWindow(s.dataDir, q.Get("territory"), since, until)
	if err == ErrNoJournal {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []WindowEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "until": until, "events": events})
}

// ---- channels -----------------------------------------------------------------

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"channels":       s.field.channels.List(),
		"evidence_tiers": EvidenceTiers,
	})
}

func (s *Server) handleChannelRegister(w http.ResponseWriter, r *http.Request) {
	ch, ok := decode[Channel](w, r)
	if !ok {
		return
	}
	if ch.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	out := s.field.channels.Register(ch)
	s.emit("channel", out)
	writeJSON(w, http.StatusOK, out)
}

// ---- watches -----------------------------------------------------------------

func (s *Server) handleWatchRegister(w http.ResponseWriter, r *http.Request) {
	wt, ok := decode[Watch](w, r)
	if !ok {
		return
	}
	if wt.Agent == "" {
		writeErr(w, http.StatusBadRequest, "agent is required")
		return
	}
	out := s.watches.Register(wt)
	s.emit("watch", out)
	writeJSON(w, http.StatusOK, map[string]any{"watch": out, "ingest_path": "/v1/ingest/" + out.ID})
}

func (s *Server) handleWatches(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"watches": s.watches.List()})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	wt, found := s.watches.Get(r.PathValue("id"))
	if !found {
		writeErr(w, http.StatusNotFound, "unknown watch")
		return
	}
	ev, ok := decode[WatchEvent](w, r)
	if !ok {
		return
	}
	sig, ok := wt.Derive(ev)
	if !ok {
		writeErr(w, http.StatusBadRequest, "event needs a resource and either a kind or a mapped outcome")
		return
	}
	s.applyRhythm(&sig)
	out := s.field.Deposit(sig)
	s.watches.touch(wt.ID)
	s.emit("signal", out)
	writeJSON(w, http.StatusOK, out)
}

// ---- territories ----------------------------------------------------------------

func (s *Server) handleTerritorySet(w http.ResponseWriter, r *http.Request) {
	t, ok := decode[Territory](w, r)
	if !ok {
		return
	}
	if t.Prefix == "" {
		writeErr(w, http.StatusBadRequest, "prefix is required")
		return
	}
	out := s.territories.Set(t.Prefix, t.Tempo)
	s.emit("territory", out)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTerritories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"territories": s.territories.List()})
}

func (s *Server) handleGradient(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	k, _ := strconv.Atoi(q.Get("k"))
	depth := -1 // leaf level unless the caller asks for a coarser scale
	if d := q.Get("depth"); d != "" {
		if n, err := strconv.Atoi(d); err == nil {
			depth = n
		}
	}
	weighted := q.Get("weighted") == "1" || q.Get("weighted") == "true"
	diffuse := q.Get("diffuse") == "1" || q.Get("diffuse") == "true"
	hs := s.field.GradientOpts(q.Get("prefix"), q.Get("kind"), k, depth, weighted, diffuse)
	if hs == nil {
		hs = []Hotspot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hotspots": hs})
}

// ---- claims --------------------------------------------------------------------

type claimReq struct {
	Agent    string  `json:"agent"`
	Resource string  `json:"resource"`
	TTLS     float64 `json:"ttl_s"`
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[claimReq](w, r)
	if !ok {
		return
	}
	if req.Agent == "" || req.Resource == "" {
		writeErr(w, http.StatusBadRequest, "agent and resource are required")
		return
	}
	ttl := time.Duration(req.TTLS * float64(time.Second))
	cl, won := s.claims.Acquire(req.Agent, req.Resource, ttl)
	s.agents.Touch(req.Agent)
	if !won {
		writeJSON(w, http.StatusConflict, map[string]any{"granted": false, "held_by": cl})
		return
	}
	if ttl <= 0 {
		ttl = DefaultClaimTTL
	}
	s.metrics.recordClaim(req.Agent, req.Resource, ttl)
	s.emit("claim", cl)
	writeJSON(w, http.StatusOK, map[string]any{"granted": true, "claim": cl})
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[claimReq](w, r)
	if !ok {
		return
	}
	released := s.claims.Release(req.Agent, req.Resource)
	if released {
		s.metrics.recordRelease(req.Resource)
		s.emit("release", map[string]string{"agent": req.Agent, "resource": req.Resource})
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": released})
}

func (s *Server) handleClaims(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"claims": s.claims.List()})
}

// ---- dances --------------------------------------------------------------------

type danceReq struct {
	Agent   string          `json:"agent"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) handleDance(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[danceReq](w, r)
	if !ok {
		return
	}
	if req.Agent == "" || req.Topic == "" {
		writeErr(w, http.StatusBadRequest, "agent and topic are required")
		return
	}
	d := s.floor.Broadcast(req.Agent, req.Topic, req.Payload)
	s.agents.Touch(req.Agent)
	s.emit("dance", d)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, _ := strconv.ParseUint(q.Get("since"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	ds := s.floor.Since(since, q.Get("topic"), limit)
	if ds == nil {
		ds = []Dance{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dances": ds})
}

// ---- memory --------------------------------------------------------------------

// memory paths look like /v1/memory/<namespace...>/<key>; the last path
// segment is the key, everything before it is the namespace.
func splitMemoryPath(rest string) (ns, key string, ok bool) {
	rest = strings.Trim(rest, "/")
	i := strings.LastIndex(rest, "/")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

func (s *Server) handleMemoryGet(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("ns")
	// listing: /v1/memory/<ns>?keys=1
	if r.URL.Query().Get("keys") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{"namespace": strings.Trim(rest, "/"), "keys": s.memory.Keys(strings.Trim(rest, "/"))})
		return
	}
	ns, key, ok := splitMemoryPath(rest)
	if !ok {
		writeErr(w, http.StatusBadRequest, "path must be /v1/memory/<namespace>/<key>")
		return
	}
	v, found := s.memory.Get(ns, key)
	if !found {
		writeErr(w, http.StatusNotFound, "no such key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": ns, "key": key, "value": v})
}

func (s *Server) handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	ns, key, ok := splitMemoryPath(r.PathValue("ns"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "path must be /v1/memory/<namespace>/<key>")
		return
	}
	var val json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&val); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	s.memory.Put(ns, key, val)
	s.emit("memory", map[string]any{"namespace": ns, "key": key, "value": val})
	writeJSON(w, http.StatusOK, map[string]any{"namespace": ns, "key": key, "stored": true})
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	ns, key, ok := splitMemoryPath(r.PathValue("ns"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "path must be /v1/memory/<namespace>/<key>")
		return
	}
	s.memory.Delete(ns, key)
	s.emit("memory_delete", map[string]string{"namespace": ns, "key": key})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// ---- events (SSE) ----------------------------------------------------------------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": waggle event stream\n\n")
	fl.Flush()

	ch, cancel := s.hub.Subscribe()
	defer cancel()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			fl.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			fl.Flush()
		}
	}
}

// ---- status, manifest, observatory ------------------------------------------------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resources, signals := s.field.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"service":   "waggle",
		"uptime_s":  time.Since(s.start).Seconds(),
		"agents":    len(s.agents.List()),
		"resources": resources,
		"signals":   signals,
		"claims":    len(s.claims.List()),
	})
}

func (s *Server) handleObservatory(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "observatory unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// replay rebuilds state from the journal. Signal deposits are replayed with
// their original timestamps so decay is preserved across restarts.
func (s *Server) replay(dir string) error {
	s.dataDir = dir
	return Replay(dir, func(typ string, data json.RawMessage) error {
		switch typ {
		case "channel":
			var ch Channel
			if err := json.Unmarshal(data, &ch); err != nil {
				return err
			}
			s.field.channels.Register(ch)
		case "watch":
			var wt Watch
			if err := json.Unmarshal(data, &wt); err != nil {
				return err
			}
			s.watches.Register(wt)
		case "territory":
			var t Territory
			if err := json.Unmarshal(data, &t); err != nil {
				return err
			}
			s.territories.Set(t.Prefix, t.Tempo)
		case "agent":
			var p Profile
			if err := json.Unmarshal(data, &p); err != nil {
				return err
			}
			s.agents.Register(p)
		case "signal":
			var sig Signal
			if err := json.Unmarshal(data, &sig); err != nil {
				return err
			}
			// Reinforcements are journaled as merged results, so a later
			// entry for the same (agent, resource, kind) supersedes earlier
			// ones rather than stacking.
			restored := sig
			s.field.mu.Lock()
			replaced := false
			for i, prev := range s.field.byRes[sig.Resource] {
				if prev.Agent == sig.Agent && prev.Kind == sig.Kind {
					s.field.byRes[sig.Resource][i] = &restored
					replaced = true
					break
				}
			}
			if !replaced {
				s.field.byRes[sig.Resource] = append(s.field.byRes[sig.Resource], &restored)
			}
			s.field.mu.Unlock()
		case "claim":
			var cl Claim
			if err := json.Unmarshal(data, &cl); err != nil {
				return err
			}
			if cl.ExpiresAt.After(time.Now()) {
				s.claims.Acquire(cl.Agent, cl.Resource, time.Until(cl.ExpiresAt))
			}
		case "dance":
			var d Dance
			if err := json.Unmarshal(data, &d); err != nil {
				return err
			}
			s.floor.Broadcast(d.Agent, d.Topic, d.Payload)
		case "memory":
			var m struct {
				Namespace string          `json:"namespace"`
				Key       string          `json:"key"`
				Value     json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
			s.memory.Put(m.Namespace, m.Key, m.Value)
		case "memory_delete":
			var m struct{ Namespace, Key string }
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
			s.memory.Delete(m.Namespace, m.Key)
		case "release":
			var m struct{ Agent, Resource string }
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
			s.claims.Release(m.Agent, m.Resource)
		}
		return nil
	})
}
