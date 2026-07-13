package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store is an append-only JSONL journal. Every mutation is one line; boot
// replays the journal to rebuild state. Signals older than their useful life
// replay to ~zero intensity and are swept immediately, so the field wakes up
// exactly as decayed as it should be.
type Store struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	path string
}

type journalEntry struct {
	Type string          `json:"type"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, nil // persistence disabled
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Store{f: f, w: bufio.NewWriter(f), path: path}, nil
}

func (s *Store) Append(typ string, data any) {
	if s == nil {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	line, _ := json.Marshal(journalEntry{Type: typ, At: time.Now(), Data: raw})
	s.mu.Lock()
	s.w.Write(line)
	s.w.WriteByte('\n')
	s.w.Flush()
	s.mu.Unlock()
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Flush()
	return s.f.Close()
}

// Replay reads the journal and applies each entry via apply(type, data).
func Replay(dir string, apply func(typ string, data json.RawMessage) error) error {
	return ReplayWithTime(dir, func(typ string, _ time.Time, data json.RawMessage) error {
		return apply(typ, data)
	})
}

// ReplayWithTime is Replay with the journal entry's wall-clock time passed
// through — needed by windowed recall, which selects entries by when they were
// journaled rather than by the signal's own deposit timestamp.
func ReplayWithTime(dir string, apply func(typ string, at time.Time, data json.RawMessage) error) error {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var e journalEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return fmt.Errorf("journal line %d: %w", line, err)
		}
		if err := apply(e.Type, e.At, e.Data); err != nil {
			return fmt.Errorf("journal line %d (%s): %w", line, e.Type, err)
		}
	}
	return sc.Err()
}
