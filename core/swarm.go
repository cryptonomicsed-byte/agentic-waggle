package main

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// ---- Agent profiles ----------------------------------------------------

// Profile is an agent's persistent identity on the substrate: who it is, what
// it wants, what it can do, and where its private memory lives.
type Profile struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Goals           []string  `json:"goals,omitempty"`
	Skills          []string  `json:"skills,omitempty"`
	MemoryNamespace string    `json:"memory_namespace"`
	RegisteredAt    time.Time `json:"registered_at"`
	LastSeen        time.Time `json:"last_seen"`
}

type Registry struct {
	mu    sync.RWMutex
	byID  map[string]*Profile
	clock func() time.Time
}

func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]*Profile), clock: time.Now}
}

// Register creates or updates an agent profile. Registration is idempotent by
// ID so a restarted agent resumes its identity (and memory namespace).
func (r *Registry) Register(p Profile) Profile {
	now := r.clock()
	if p.ID == "" {
		p.ID = newID()
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.MemoryNamespace == "" {
		p.MemoryNamespace = "agent/" + p.ID
	}
	r.mu.Lock()
	if prev, ok := r.byID[p.ID]; ok {
		p.RegisteredAt = prev.RegisteredAt
		if len(p.Goals) == 0 {
			p.Goals = prev.Goals
		}
		if len(p.Skills) == 0 {
			p.Skills = prev.Skills
		}
	} else {
		p.RegisteredAt = now
	}
	p.LastSeen = now
	cp := p
	r.byID[p.ID] = &cp
	r.mu.Unlock()
	return p
}

func (r *Registry) Touch(id string) {
	r.mu.Lock()
	if p, ok := r.byID[id]; ok {
		p.LastSeen = r.clock()
	}
	r.mu.Unlock()
}

func (r *Registry) Get(id string) (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.byID[id]; ok {
		return *p, true
	}
	return Profile{}, false
}

func (r *Registry) List() []Profile {
	r.mu.RLock()
	out := make([]Profile, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, *p)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// ---- Claims (leases) -----------------------------------------------------

// Claim is a time-bounded exclusive lease on a resource. Claims are how the
// swarm avoids duplicated work without an orchestrator: sniff, then claim,
// then act. Expiry means a crashed agent can never wedge a resource.
type Claim struct {
	Resource  string    `json:"resource"`
	Agent     string    `json:"agent"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Claims struct {
	mu    sync.Mutex
	byRes map[string]*Claim
	clock func() time.Time
}

func NewClaims() *Claims {
	return &Claims{byRes: make(map[string]*Claim), clock: time.Now}
}

const DefaultClaimTTL = 300 * time.Second

// Acquire takes or renews a lease. It returns the winning claim and whether
// the caller holds it. Renewals by the current holder extend the lease.
func (c *Claims) Acquire(agent, resource string, ttl time.Duration) (Claim, bool) {
	if ttl <= 0 {
		ttl = DefaultClaimTTL
	}
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.byRes[resource]
	if ok && cur.ExpiresAt.After(now) && cur.Agent != agent {
		return *cur, false
	}
	cl := &Claim{Resource: resource, Agent: agent, ClaimedAt: now, ExpiresAt: now.Add(ttl)}
	if ok && cur.Agent == agent && cur.ExpiresAt.After(now) {
		cl.ClaimedAt = cur.ClaimedAt
	}
	c.byRes[resource] = cl
	return *cl, true
}

// Release drops a lease if held by agent. Returns true if a live lease was
// released.
func (c *Claims) Release(agent, resource string) bool {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.byRes[resource]
	if !ok {
		return false
	}
	if cur.Agent != agent {
		return false
	}
	delete(c.byRes, resource)
	return cur.ExpiresAt.After(now)
}

func (c *Claims) List() []Claim {
	now := c.clock()
	c.mu.Lock()
	out := make([]Claim, 0, len(c.byRes))
	for res, cl := range c.byRes {
		if cl.ExpiresAt.After(now) {
			out = append(out, *cl)
		} else {
			delete(c.byRes, res)
		}
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out
}

// ---- Dances (broadcasts) ---------------------------------------------------

// Dance is the direct-broadcast channel, named for the bee waggle dance: an
// agent that found something worth the whole swarm's attention announces it.
// Dances complement signals: signals are ambient and place-bound, dances are
// immediate and topic-bound.
type Dance struct {
	Seq     uint64          `json:"seq"`
	ID      string          `json:"id"`
	Agent   string          `json:"agent"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
	At      time.Time       `json:"at"`
}

type DanceFloor struct {
	mu    sync.RWMutex
	ring  []Dance
	next  uint64
	size  int
	clock func() time.Time
}

func NewDanceFloor(size int) *DanceFloor {
	if size <= 0 {
		size = 1000
	}
	return &DanceFloor{size: size, clock: time.Now, next: 1}
}

func (d *DanceFloor) Broadcast(agent, topic string, payload json.RawMessage) Dance {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	d.mu.Lock()
	dance := Dance{Seq: d.next, ID: newID(), Agent: agent, Topic: topic, Payload: payload, At: d.clock()}
	d.next++
	d.ring = append(d.ring, dance)
	if len(d.ring) > d.size {
		d.ring = d.ring[len(d.ring)-d.size:]
	}
	d.mu.Unlock()
	return dance
}

// Since returns dances with Seq > since, optionally filtered by topic.
func (d *DanceFloor) Since(since uint64, topic string, limit int) []Dance {
	if limit <= 0 {
		limit = 100
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []Dance
	for _, dn := range d.ring {
		if dn.Seq <= since {
			continue
		}
		if topic != "" && dn.Topic != topic {
			continue
		}
		out = append(out, dn)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ---- Memory (namespaced KV) ------------------------------------------------

// Memory is durable namespaced key-value storage. Each agent gets a private
// namespace from its profile; shared namespaces (e.g. "swarm/plan") are just
// namespaces every agent agrees to use.
type Memory struct {
	mu sync.RWMutex
	ns map[string]map[string]json.RawMessage
}

func NewMemory() *Memory {
	return &Memory{ns: make(map[string]map[string]json.RawMessage)}
}

func (m *Memory) Put(ns, key string, val json.RawMessage) {
	m.mu.Lock()
	bucket := m.ns[ns]
	if bucket == nil {
		bucket = make(map[string]json.RawMessage)
		m.ns[ns] = bucket
	}
	bucket[key] = val
	m.mu.Unlock()
}

func (m *Memory) Get(ns, key string) (json.RawMessage, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.ns[ns][key]
	return v, ok
}

func (m *Memory) Delete(ns, key string) {
	m.mu.Lock()
	delete(m.ns[ns], key)
	m.mu.Unlock()
}

func (m *Memory) Keys(ns string) []string {
	m.mu.RLock()
	out := make([]string, 0, len(m.ns[ns]))
	for k := range m.ns[ns] {
		out = append(out, k)
	}
	m.mu.RUnlock()
	sort.Strings(out)
	return out
}
