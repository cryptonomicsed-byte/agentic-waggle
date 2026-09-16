package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func doJSON(t *testing.T, ts *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, &buf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPIEndToEnd(t *testing.T) {
	ts := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts.Close()

	// register two agents
	code, scout := doJSON(t, ts, "POST", "/v1/agents", map[string]any{
		"id": "scout-1", "name": "Scout", "skills": []string{"search"}, "goals": []string{"find nectar"}})
	if code != 200 || scout["memory_namespace"] != "agent/scout-1" {
		t.Fatalf("register: %d %v", code, scout)
	}
	doJSON(t, ts, "POST", "/v1/agents", map[string]any{"id": "worker-1", "name": "Worker"})

	// deposit and sniff
	code, _ = doJSON(t, ts, "POST", "/v1/signals", map[string]any{
		"agent": "scout-1", "resource": "repo://src/auth.go", "kind": "gold", "intensity": 5, "note": "vuln here"})
	if code != 200 {
		t.Fatalf("deposit: %d", code)
	}
	code, sniff := doJSON(t, ts, "GET", "/v1/sniff?resource=repo://src/auth.go", nil)
	sigs := sniff["signals"].([]any)
	if code != 200 || len(sigs) != 1 {
		t.Fatalf("sniff: %d %v", code, sniff)
	}
	if sigs[0].(map[string]any)["note"] != "vuln here" {
		t.Fatalf("sniff lost note: %v", sigs[0])
	}

	// missing fields rejected
	if code, _ := doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "scout-1"}); code != 400 {
		t.Fatalf("bad deposit want 400, got %d", code)
	}

	// gradient sees the hotspot
	code, grad := doJSON(t, ts, "GET", "/v1/gradient?prefix=repo://", nil)
	if code != 200 || len(grad["hotspots"].([]any)) != 1 {
		t.Fatalf("gradient: %d %v", code, grad)
	}

	// claims: contention returns 409 with holder
	code, cl := doJSON(t, ts, "POST", "/v1/claims", map[string]any{"agent": "scout-1", "resource": "task://1", "ttl_s": 60})
	if code != 200 || cl["granted"] != true {
		t.Fatalf("claim: %d %v", code, cl)
	}
	code, cl = doJSON(t, ts, "POST", "/v1/claims", map[string]any{"agent": "worker-1", "resource": "task://1"})
	if code != 409 || cl["granted"] != false {
		t.Fatalf("contended claim want 409, got %d %v", code, cl)
	}
	code, rel := doJSON(t, ts, "POST", "/v1/claims/release", map[string]any{"agent": "scout-1", "resource": "task://1"})
	if code != 200 || rel["released"] != true {
		t.Fatalf("release: %d %v", code, rel)
	}

	// dances with cursor
	doJSON(t, ts, "POST", "/v1/dances", map[string]any{"agent": "scout-1", "topic": "found", "payload": map[string]any{"where": "auth.go"}})
	code, ds := doJSON(t, ts, "GET", "/v1/dances?since=0&topic=found", nil)
	if code != 200 || len(ds["dances"].([]any)) != 1 {
		t.Fatalf("dances: %d %v", code, ds)
	}

	// memory: nested namespace, roundtrip
	code, _ = doJSON(t, ts, "PUT", "/v1/memory/agent/scout-1/plan", map[string]any{"step": 3})
	if code != 200 {
		t.Fatalf("memory put: %d", code)
	}
	code, mem := doJSON(t, ts, "GET", "/v1/memory/agent/scout-1/plan", nil)
	if code != 200 || mem["value"].(map[string]any)["step"] != float64(3) {
		t.Fatalf("memory get: %d %v", code, mem)
	}
	if code, _ := doJSON(t, ts, "GET", "/v1/memory/agent/scout-1/nope", nil); code != 404 {
		t.Fatalf("missing key want 404, got %d", code)
	}

	// manifest is discoverable and lists actions
	code, man := doJSON(t, ts, "GET", "/.well-known/waggle.json", nil)
	if code != 200 || man["protocol"] != "waggle/v1" || len(man["actions"].([]any)) < 10 {
		t.Fatalf("manifest: %d %v", code, man["protocol"])
	}

	// observatory serves HTML
	resp, err := http.Get(ts.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("observatory: %v %v", err, resp)
	}
	resp.Body.Close()
}

func TestJournalReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")

	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, ServerConfig{})
	ts := httptest.NewServer(srv)

	doJSON(t, ts, "POST", "/v1/agents", map[string]any{"id": "scout-1", "name": "Scout"})
	doJSON(t, ts, "POST", "/v1/signals", map[string]any{
		"agent": "scout-1", "resource": "repo://x", "kind": "gold", "intensity": 6, "half_life_s": 3600})
	// reinforce: journal now has two entries for the same signal
	doJSON(t, ts, "POST", "/v1/signals", map[string]any{
		"agent": "scout-1", "resource": "repo://x", "kind": "gold", "intensity": 2, "half_life_s": 3600})
	doJSON(t, ts, "PUT", "/v1/memory/agent/scout-1/note", "remember")
	ts.Close()
	store.Close()

	// cold start: replay the journal
	srv2 := NewServer(nil, ServerConfig{})
	if err := srv2.replay(dir); err != nil {
		t.Fatalf("replay: %v", err)
	}
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()

	if _, ok := srv2.agents.Get("scout-1"); !ok {
		t.Fatal("agent lost across restart")
	}
	code, sniff := doJSON(t, ts2, "GET", "/v1/sniff?resource=repo://x", nil)
	sigs := sniff["signals"].([]any)
	if code != 200 || len(sigs) != 1 {
		t.Fatalf("replayed signals want 1 merged, got %v", sniff)
	}
	if got := sigs[0].(map[string]any)["intensity"].(float64); got < 7.5 || got > 8 {
		t.Fatalf("replayed intensity want ~8, got %v", got)
	}
	code, mem := doJSON(t, ts2, "GET", "/v1/memory/agent/scout-1/note", nil)
	if code != 200 || mem["value"] != "remember" {
		t.Fatalf("memory lost across restart: %d %v", code, mem)
	}
}

func TestSSEEventStream(t *testing.T) {
	ts := httptest.NewServer(NewServer(nil, ServerConfig{}))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type: %s", ct)
	}

	// read the greeting, then trigger an event and see it arrive
	buf := make([]byte, 4096)
	resp.Body.Read(buf) // ": waggle event stream"

	doJSON(t, ts, "POST", "/v1/signals", map[string]any{"agent": "a1", "resource": "r", "kind": "explored"})

	done := make(chan string, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()
	select {
	case chunk := <-done:
		if !bytes.Contains([]byte(chunk), []byte("event: signal")) {
			t.Fatalf("SSE chunk missing signal event: %q", chunk)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no SSE event within 3s")
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
