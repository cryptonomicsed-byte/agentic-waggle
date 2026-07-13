package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrNoJournal is returned when recall is asked of a substrate running
// without persistence — there is no history to consult.
var ErrNoJournal = errors.New("recall requires a journal (start waggled with -data)")

// WindowEvent is one journaled deposit within a recall window: not the merged
// final state (that is RecallAt) but the individual transitions, so a consumer
// can see a territory's *trajectory* over time — what went hot, what cooled,
// what got tabooed and when. This is the raw material the field digest (round
// 2, #5) summarizes for humans.
type WindowEvent struct {
	Signal Signal    `json:"signal"`
	At     time.Time `json:"at"` // journal entry time
}

// RecallWindow returns every signal deposit under `prefix` journaled in the
// half-open window [since, until), oldest first. Unlike RecallAt it does not
// collapse to last-per-key — the digest needs the sequence, not the snapshot.
// A whole territory's history in one call, so a digest need not do N lookups.
func RecallWindow(dir, prefix string, since, until time.Time) ([]WindowEvent, error) {
	if dir == "" {
		return nil, ErrNoJournal
	}
	var out []WindowEvent
	err := ReplayWithTime(dir, func(typ string, at time.Time, data json.RawMessage) error {
		if typ != "signal" {
			return nil
		}
		if at.Before(since) || !at.Before(until) {
			return nil
		}
		var sig Signal
		if err := json.Unmarshal(data, &sig); err != nil {
			return err
		}
		if prefix != "" && !strings.HasPrefix(sig.Resource, prefix) {
			return nil
		}
		out = append(out, WindowEvent{Signal: sig, At: at})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// RecallAt reconstructs the field's state as it stood at a past instant, by
// replaying the journal up to that time. It is the second read path into the
// journal: sniff answers "what does the field say now", recall answers "what
// did it say then". Because reinforcements are journaled as merged results, a
// later entry for the same (agent, resource, kind) supersedes earlier ones —
// the same rule boot replay uses.
//
// Intensities are decayed to the recall instant, not to now: recall ignores
// live decay, not the decay physics of the moment being asked about.
func RecallAt(dir string, at time.Time, q SniffQuery) ([]Signal, error) {
	if dir == "" {
		return nil, ErrNoJournal
	}
	type key struct{ agent, resource, kind string }
	last := make(map[key]Signal)

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
		if q.Resource != "" && sig.Resource != q.Resource {
			return nil
		}
		if q.Prefix != "" && !strings.HasPrefix(sig.Resource, q.Prefix) {
			return nil
		}
		if q.Kind != "" && sig.Kind != q.Kind {
			return nil
		}
		if q.Agent != "" && sig.Agent != q.Agent {
			return nil
		}
		last[key{sig.Agent, sig.Resource, sig.Kind}] = sig
		return nil
	})
	if err != nil {
		return nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	out := make([]Signal, 0, len(last))
	for _, sig := range last {
		cur := sig.At(at)
		if cur < q.Min { // recall has no implicit evaporation floor: history includes the faded
			continue
		}
		snap := sig
		snap.Intensity = cur
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Intensity > out[j].Intensity })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
