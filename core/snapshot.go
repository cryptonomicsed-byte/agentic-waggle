package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Snapshot is a portable, content-addressed capture of a slice of the field:
// the signal state under a URI prefix at a past instant. It is the environment
// analogue of PALIMPSEST's deterministic cognition replay (Connection Map v2
// round 2, #6) — instead of replaying an agent's thinking, it replays the
// exact scent state that a decision was made against, so emergent behavior can
// be reproduced and inspected after the fact.
//
// Because it carries original deposit timestamps, loading a snapshot into a
// fresh daemon reconstructs exact decay state, not a re-timestamped copy. Two
// snapshots of identical state hash identically, so they diff cleanly.
type Snapshot struct {
	Prefix    string    `json:"prefix"`
	At        time.Time `json:"at"`
	CreatedAt time.Time `json:"created_at"`
	Hash      string    `json:"hash"` // content address: sha256 over the sorted signals
	Signals   []Signal  `json:"signals"`
}

// ExportSnapshot replays the journal to `at` and captures the last signal per
// (agent, resource, kind) under `prefix`, preserving deposit timestamps. It is
// the territory-scoped, time-bounded partial replay the full-daemon replay
// generalizes. Requires a journal (returns ErrNoJournal otherwise).
func ExportSnapshot(dir, prefix string, at time.Time) (Snapshot, error) {
	if dir == "" {
		return Snapshot{}, ErrNoJournal
	}
	type key struct{ agent, resource, kind string }
	last := map[key]Signal{}
	err := Replay(dir, func(typ string, data json.RawMessage) error {
		if typ != "signal" {
			return nil
		}
		var sig Signal
		if err := json.Unmarshal(data, &sig); err != nil {
			return err
		}
		if sig.DepositedAt.After(at) {
			return nil
		}
		if prefix != "" && !strings.HasPrefix(sig.Resource, prefix) {
			return nil
		}
		last[key{sig.Agent, sig.Resource, sig.Kind}] = sig
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	sigs := make([]Signal, 0, len(last))
	for _, s := range last {
		sigs = append(sigs, s)
	}
	// deterministic order so the content hash is stable
	sort.Slice(sigs, func(i, j int) bool {
		if sigs[i].Resource != sigs[j].Resource {
			return sigs[i].Resource < sigs[j].Resource
		}
		if sigs[i].Agent != sigs[j].Agent {
			return sigs[i].Agent < sigs[j].Agent
		}
		return sigs[i].Kind < sigs[j].Kind
	})
	return Snapshot{
		Prefix:    prefix,
		At:        at,
		CreatedAt: time.Now(),
		Hash:      hashSignals(sigs),
		Signals:   sigs,
	}, nil
}

// hashSignals content-addresses the signal set. CreatedAt is deliberately
// excluded so identical field state hashes identically regardless of when it
// was exported.
func hashSignals(sigs []Signal) string {
	h := sha256.New()
	for _, s := range sigs {
		// a stable, timestamp-preserving projection of the load-bearing fields
		fmtInto(h, s.Agent, s.Resource, s.Kind, s.Subtype, s.EvidenceTier, s.Decay)
		b, _ := json.Marshal([]any{s.Intensity, s.HalfLifeS, s.Alpha, s.DepositedAt.UnixNano()})
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fmtInto(h interface{ Write([]byte) (int, error) }, parts ...string) {
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
}

// LoadSnapshot ingests a snapshot into the live field, preserving each
// signal's original deposit timestamp so decay resumes exactly where the
// snapshot captured it — the same mechanism boot replay uses, applied to an
// imported slice rather than the local journal. Returns how many signals
// loaded.
func (f *Field) LoadSnapshot(snap Snapshot) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, sig := range snap.Signals {
		restored := sig
		if restored.ID == "" {
			restored.ID = newID()
		}
		replaced := false
		for i, prev := range f.byRes[sig.Resource] {
			if prev.Agent == sig.Agent && prev.Kind == sig.Kind {
				f.byRes[sig.Resource][i] = &restored
				replaced = true
				break
			}
		}
		if !replaced {
			f.byRes[sig.Resource] = append(f.byRes[sig.Resource], &restored)
		}
		n++
	}
	return n
}

// ---- handlers ----

func (s *Server) handleSnapshotExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	at := time.Now()
	if v := q.Get("at"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "at must be RFC3339: "+err.Error())
			return
		}
		at = parsed
	}
	snap, err := ExportSnapshot(s.dataDir, q.Get("prefix"), at)
	if err == ErrNoJournal {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleSnapshotLoad(w http.ResponseWriter, r *http.Request) {
	snap, ok := decode[Snapshot](w, r)
	if !ok {
		return
	}
	// verify the content address before trusting an imported slice
	if snap.Hash != "" && snap.Hash != hashSignals(snap.Signals) {
		writeErr(w, http.StatusBadRequest, "snapshot hash mismatch: content does not match its content address")
		return
	}
	n := s.field.LoadSnapshot(snap)
	// journal the imported signals so they survive this daemon's own restart
	for _, sig := range snap.Signals {
		s.store.Append("signal", sig)
	}
	writeJSON(w, http.StatusOK, map[string]any{"loaded": n, "hash": snap.Hash})
}
