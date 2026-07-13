//! wag — shell-native client for the Waggle stigmergic coordination substrate.
//!
//! Agents that live in a shell (Claude Code, CI jobs, cron-driven daemons)
//! coordinate through this one static binary: sniff before acting, mark after
//! acting, claim before exclusive work. Std-only on purpose — no crates, no
//! network at build time, nothing to install but the binary.
//!
//! Identity comes from --agent or $WAGGLE_AGENT; the substrate address from
//! $WAGGLE_HOST (default 127.0.0.1:7777). Output is the substrate's JSON,
//! untouched, so it pipes cleanly into jq or back into another agent.

use std::collections::HashMap;
use std::env;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::process::exit;

const USAGE: &str = r#"wag — stigmergic coordination for shell-native agents

usage: wag <command> [args] [--flags]

  register              create/resume this agent's profile
                          --id ID --name NAME --skills a,b --goals "g1,g2"
  mark <resource> <kind>  deposit a decaying signal
                          --intensity N --half-life SECS --note TEXT
                          --decay exp|power (power = heavy tail: fades to
                          background, not to nothing) --alpha N
                          --subtype S --tier self-report|corroborated|
                          watch-derived|zangbeto-verified|on-chain-anchored
                          --cost-tokens N --cost-ms N --cost-dollars N
                          --cost-producer NAME --cost-method M --cost-units U
                          --capability TOKEN (taboo only: Èṣù-signed grant)
  sniff                 read the field
                          --resource URI | --prefix URI [--kind K] [--min N]
                          [--min-tier T  drop signals below an evidence tier]
                          [--optimize cost_efficiency  rank cheap-gold first]
                          [--limit N]
  batch <uri> [uri...]  gradient rollups for many URIs in one call
                          [--kind K] [--weighted]
  gradient              ranked hotspots: where is the swarm's attention?
                          [--prefix URI] [--kind K] [--k N]
                          [--depth N]  roll up to URI-tree level N and zoom
                          in coarse-to-fine (0=scheme, 1=first segment, ...)
                          [--weighted  trust-adjusted totals]
                          [--diffuse   5% sibling bleed at leaf level]
  explain <resource>    why does this resource read the way it does:
                          tier weights, cross-inhibitions, diffusion
  recall <resource>     the field as it stood at a past instant
                          --at RFC3339 [--prefix] (needs waggled -data)
  channels [list]       typed channels + the evidence-tier ladder
  replay <journal>      re-emit a journal's events to stdout for post-mortem
                          review (--speed events/sec, default 50)
  snapshot export       export a portable, content-addressed field slice
                          [--prefix URI] [--at RFC3339] [-o FILE] (needs -data)
  snapshot replay <file>  load a snapshot into a (fresh) daemon, preserving
                          decay state — reproduce a past decision in isolation
  attack-sim <scenario> run a red-team scenario against the field
                          scenario: sybil | taboo-grief | lease-squat | all
                          (needs waggled -debug; scriptable in CI)
  claim <resource>      acquire/renew an exclusive lease (--ttl SECS)
                          exit 0 granted, exit 3 held by another agent
  release <resource>    release a held lease
  claims                list live leases
  dance <topic>         broadcast to the swarm (--payload JSON)
  dances                poll broadcasts (--since SEQ --topic T)
  mem put <ns> <key> <json>   store durable memory
  mem get <ns> <key>          fetch durable memory
  mem keys <ns>               list keys in a namespace
  agents                list swarm members
  status                substrate health
  manifest              the substrate's self-description (all actions)
  watch                 stream live events (SSE) to stdout

environment: WAGGLE_HOST (default 127.0.0.1:7777), WAGGLE_AGENT
signal kinds: explored gold dead-end help warn handoff claimed heartbeat"#;

fn main() {
    let argv: Vec<String> = env::args().skip(1).collect();
    if argv.is_empty() {
        eprintln!("{USAGE}");
        exit(64);
    }
    let (pos, flags) = parse(&argv[1..]);
    let host = env::var("WAGGLE_HOST").unwrap_or_else(|_| "127.0.0.1:7777".into());

    let result = match argv[0].as_str() {
        "register" => register(&host, &flags),
        "mark" => mark(&host, &pos, &flags),
        "sniff" => get(&host, &format!("/v1/sniff{}", query(&flags, &[("resource", "resource"), ("prefix", "prefix"), ("kind", "kind"), ("agent", "agent"), ("min", "min"), ("min-tier", "min_tier"), ("optimize", "optimize"), ("limit", "limit")]))),
        "batch" => batch(&host, &pos, &flags),
        "gradient" => get(&host, &format!("/v1/gradient{}", query(&flags, &[("prefix", "prefix"), ("kind", "kind"), ("k", "k"), ("depth", "depth"), ("weighted", "weighted"), ("diffuse", "diffuse")]))),
        "explain" => explain(&host, &pos),
        "recall" => recall(&host, &pos, &flags),
        "channels" => get(&host, "/v1/channels"),
        "replay" => replay(&pos, &flags),
        "attack-sim" => attack_sim(&host, &pos),
        "snapshot" => snapshot(&host, &pos, &flags),
        "claim" => claim(&host, &pos, &flags),
        "release" => release(&host, &pos, &flags),
        "claims" => get(&host, "/v1/claims"),
        "dance" => dance(&host, &pos, &flags),
        "dances" => get(&host, &format!("/v1/dances{}", query(&flags, &[("since", "since"), ("topic", "topic"), ("limit", "limit")]))),
        "mem" => mem(&host, &pos),
        "agents" => get(&host, "/v1/agents"),
        "status" => get(&host, "/v1/status"),
        "manifest" => get(&host, "/.well-known/waggle.json"),
        "watch" => watch(&host),
        "help" | "--help" | "-h" => {
            println!("{USAGE}");
            return;
        }
        other => {
            eprintln!("wag: unknown command '{other}'\n\n{USAGE}");
            exit(64);
        }
    };

    match result {
        Ok((code, body)) => {
            println!("{}", body.trim_end());
            match code {
                200..=299 => {}
                409 => exit(3), // claim contention: distinct, scriptable exit code
                _ => exit(1),
            }
        }
        Err(e) => {
            eprintln!("wag: {e}");
            exit(1);
        }
    }
}

// ---- commands -------------------------------------------------------------

type Flags = HashMap<String, String>;
type Out = Result<(u16, String), String>;

fn agent(flags: &Flags) -> Result<String, String> {
    flags
        .get("agent")
        .cloned()
        .or_else(|| env::var("WAGGLE_AGENT").ok())
        .ok_or_else(|| "no agent identity: pass --agent or set WAGGLE_AGENT".into())
}

fn register(host: &str, flags: &Flags) -> Out {
    let id = flags
        .get("id")
        .cloned()
        .or_else(|| env::var("WAGGLE_AGENT").ok())
        .unwrap_or_default();
    let mut body = JsonObj::new();
    if !id.is_empty() {
        body.str("id", &id);
    }
    if let Some(n) = flags.get("name") {
        body.str("name", n);
    }
    if let Some(s) = flags.get("skills") {
        body.str_array("skills", s.split(',').map(str::trim));
    }
    if let Some(g) = flags.get("goals") {
        body.str_array("goals", g.split(',').map(str::trim));
    }
    request(host, "POST", "/v1/agents", Some(&body.finish()))
}

fn mark(host: &str, pos: &[String], flags: &Flags) -> Out {
    let [resource, kind] = two(pos, "mark <resource> <kind>")?;
    let mut body = JsonObj::new();
    body.str("agent", &agent(flags)?);
    body.str("resource", resource);
    body.str("kind", kind);
    if let Some(v) = flags.get("intensity") {
        body.raw("intensity", &num(v, "--intensity")?);
    }
    if let Some(v) = flags.get("half-life") {
        body.raw("half_life_s", &num(v, "--half-life")?);
    }
    if let Some(d) = flags.get("decay") {
        body.str("decay", d);
    }
    if let Some(v) = flags.get("alpha") {
        body.raw("alpha", &num(v, "--alpha")?);
    }
    if let Some(s) = flags.get("subtype") {
        body.str("subtype", s);
    }
    if let Some(t) = flags.get("tier") {
        body.str("evidence_tier", t);
    }
    // cost: what producing this finding cost (drives sniff --optimize cost)
    let mut cost = JsonObj::new();
    let mut has_cost = false;
    if let Some(v) = flags.get("cost-tokens") {
        cost.raw("tokens", &num(v, "--cost-tokens")?);
        has_cost = true;
    }
    if let Some(v) = flags.get("cost-ms") {
        cost.raw("wall_clock_ms", &num(v, "--cost-ms")?);
        has_cost = true;
    }
    if let Some(v) = flags.get("cost-dollars") {
        cost.raw("dollars", &num(v, "--cost-dollars")?);
        has_cost = true;
    }
    // cost provenance: who measured the numbers and how, so an efficiency
    // ranking can be audited rather than trusted blind
    if flags.contains_key("cost-producer") || flags.contains_key("cost-method") {
        let mut src = JsonObj::new();
        if let Some(p) = flags.get("cost-producer") {
            src.str("producer", p);
        }
        if let Some(mth) = flags.get("cost-method") {
            src.str("method", mth);
        }
        if let Some(u) = flags.get("cost-units") {
            src.str("units", u);
        }
        cost.raw("source", &src.finish());
        has_cost = true;
    }
    if has_cost {
        body.raw("cost", &cost.finish());
    }
    if let Some(n) = flags.get("note") {
        body.str("note", n);
    }
    // taboo only: an Èṣù-signed capability authorizing the censor. Ignored by
    // other channels; required by a daemon started with -taboo-auth-enforce.
    if let Some(cap) = flags.get("capability") {
        body.str("capability", cap);
    }
    request(host, "POST", "/v1/signals", Some(&body.finish()))
}

fn batch(host: &str, pos: &[String], flags: &Flags) -> Out {
    if pos.is_empty() {
        return Err("usage: wag batch <uri> [uri...] [--kind K] [--weighted]".into());
    }
    let mut body = JsonObj::new();
    body.str_array("uris", pos.iter().map(String::as_str));
    if let Some(k) = flags.get("kind") {
        body.str("kind", k);
    }
    if flags.contains_key("weighted") {
        body.raw("weighted", "true");
    }
    request(host, "POST", "/v1/sniff/batch", Some(&body.finish()))
}

fn explain(host: &str, pos: &[String]) -> Out {
    let [resource] = one(pos, "explain <resource>")?;
    get(host, &format!("/v1/explain?resource={}", urlenc(resource)))
}

fn recall(host: &str, pos: &[String], flags: &Flags) -> Out {
    let at = flags
        .get("at")
        .ok_or("recall needs --at <RFC3339 instant>")?;
    let mut q = format!("/v1/recall?at={}", urlenc(at));
    if let Some(p) = flags.get("prefix") {
        q.push_str(&format!("&prefix={}", urlenc(p)));
    } else {
        let [resource] = one(pos, "recall <resource> --at <instant>")?;
        q.push_str(&format!("&resource={}", urlenc(resource)));
    }
    if let Some(k) = flags.get("kind") {
        q.push_str(&format!("&kind={}", urlenc(k)));
    }
    get(host, &q)
}

/// snapshot export/replay: portable, content-addressed field slices (round 2,
/// #6). export pulls a territory/time-scoped capture (with preserved decay
/// timestamps) to a file or stdout; replay loads one into a target daemon —
/// point it at a fresh `waggled` to reproduce a past decision in isolation.
fn snapshot(host: &str, pos: &[String], flags: &Flags) -> Out {
    match pos.first().map(String::as_str) {
        Some("export") => {
            let mut path = "/v1/snapshot".to_string();
            let q = query(flags, &[("prefix", "prefix"), ("at", "at")]);
            path.push_str(&q);
            let (code, body) = request(host, "GET", &path, None)?;
            if code == 200 {
                if let Some(out) = flags.get("o") {
                    std::fs::write(out, &body).map_err(|e| format!("write {out}: {e}"))?;
                    return Ok((200, format!("{{\"exported\":true,\"file\":\"{}\"}}", esc(out))));
                }
            }
            Ok((code, body))
        }
        Some("replay") => {
            let file = pos
                .get(1)
                .ok_or("usage: wag snapshot replay <file> [--addr host]")?;
            let body = std::fs::read_to_string(file).map_err(|e| format!("read {file}: {e}"))?;
            request(host, "POST", "/v1/snapshot/load", Some(&body))
        }
        _ => Err("usage: wag snapshot export [--prefix URI] [--at RFC3339] [-o FILE] | wag snapshot replay <file>".into()),
    }
}

/// replay re-emits a journal's entries to stdout at a steady pace, so a human
/// (or a downstream pipe, e.g. into an Observatory feed) can review how a
/// hotspot formed after the fact. Pure client-side: reads the JSONL file the
/// daemon wrote, no server involvement.
fn replay(pos: &[String], flags: &Flags) -> Out {
    let [path] = one(pos, "replay <journal.jsonl> [--speed events/sec]")?;
    let speed: f64 = flags
        .get("speed")
        .map(|v| v.parse().map_err(|_| format!("--speed expects a number, got '{v}'")))
        .transpose()?
        .unwrap_or(50.0);
    if speed <= 0.0 {
        return Err("--speed must be positive".into());
    }
    let data = std::fs::read_to_string(path).map_err(|e| format!("read {path}: {e}"))?;
    let pause = std::time::Duration::from_secs_f64(1.0 / speed);
    let mut stdout = std::io::stdout();
    for line in data.lines() {
        if line.is_empty() {
            continue;
        }
        writeln!(stdout, "{line}").map_err(|e| e.to_string())?;
        stdout.flush().ok();
        std::thread::sleep(pause);
    }
    Ok((200, String::new()))
}

fn claim(host: &str, pos: &[String], flags: &Flags) -> Out {
    let [resource] = one(pos, "claim <resource>")?;
    let mut body = JsonObj::new();
    body.str("agent", &agent(flags)?);
    body.str("resource", resource);
    if let Some(v) = flags.get("ttl") {
        body.raw("ttl_s", &num(v, "--ttl")?);
    }
    request(host, "POST", "/v1/claims", Some(&body.finish()))
}

fn release(host: &str, pos: &[String], flags: &Flags) -> Out {
    let [resource] = one(pos, "release <resource>")?;
    let mut body = JsonObj::new();
    body.str("agent", &agent(flags)?);
    body.str("resource", resource);
    request(host, "POST", "/v1/claims/release", Some(&body.finish()))
}

fn dance(host: &str, pos: &[String], flags: &Flags) -> Out {
    let [topic] = one(pos, "dance <topic>")?;
    let mut body = JsonObj::new();
    body.str("agent", &agent(flags)?);
    body.str("topic", topic);
    if let Some(p) = flags.get("payload") {
        body.raw("payload", p); // caller-supplied JSON passes through verbatim
    }
    request(host, "POST", "/v1/dances", Some(&body.finish()))
}

fn mem(host: &str, pos: &[String]) -> Out {
    match pos {
        [op, ns, key, val] if op == "put" => request(
            host,
            "PUT",
            &format!("/v1/memory/{ns}/{key}"),
            Some(val),
        ),
        [op, ns, key] if op == "get" => get(host, &format!("/v1/memory/{ns}/{key}")),
        [op, ns] if op == "keys" => get(host, &format!("/v1/memory/{ns}?keys=1")),
        _ => Err("usage: wag mem put <ns> <key> <json> | mem get <ns> <key> | mem keys <ns>".into()),
    }
}

fn get(host: &str, path: &str) -> Out {
    request(host, "GET", path, None)
}

/// watch streams the SSE feed straight to stdout, line-buffered, until the
/// server closes or the process is killed. This is how a shell agent tails
/// the swarm: `wag watch | while read line; do ...; done`
fn watch(host: &str) -> Out {
    let mut stream = TcpStream::connect(host).map_err(|e| format!("connect {host}: {e}"))?;
    // HTTP/1.0 so the server streams the body unframed (no chunk sizes to
    // strip) and closes the connection when done.
    write!(
        stream,
        "GET /v1/events HTTP/1.0\r\nHost: {host}\r\nAccept: text/event-stream\r\n\r\n"
    )
    .map_err(|e| e.to_string())?;
    let mut buf = [0u8; 4096];
    let mut headers_done = false;
    let mut pending: Vec<u8> = Vec::new();
    let mut stdout = std::io::stdout();
    loop {
        let n = stream.read(&mut buf).map_err(|e| e.to_string())?;
        if n == 0 {
            return Ok((200, String::new()));
        }
        if headers_done {
            stdout.write_all(&buf[..n]).map_err(|e| e.to_string())?;
            stdout.flush().ok();
            continue;
        }
        pending.extend_from_slice(&buf[..n]);
        if let Some(split) = find_headers_end(&pending) {
            headers_done = true;
            stdout
                .write_all(&pending[split + 4..])
                .map_err(|e| e.to_string())?;
            stdout.flush().ok();
            pending.clear();
        }
    }
}

// ---- attack-sim: red-team scenarios (round 2, #1) ----------------------------
//
// Native Rust so red-team runs are scriptable in CI with real exit codes and
// zero extra dependencies. Generates hostile traffic against the field, then
// grades the defense by reading /v1/debug/attack-metrics (needs waggled
// -debug). The richer scorer with detailed output lives in
// sdk/redteam/redteam.py; this is the shell-native, exit-code-driven twin.
//
// Exit 0 = all requested defenses held; exit 1 = a defense failed or the
// field/metrics were unreachable.
fn attack_sim(host: &str, pos: &[String]) -> ! {
    let scenario = pos.first().map(String::as_str).unwrap_or("all");
    let scenarios: Vec<&str> = match scenario {
        "all" => vec!["sybil", "taboo-grief", "lease-squat"],
        s => vec![s],
    };
    // fail fast if -debug metrics aren't available
    match request(host, "GET", "/v1/debug/attack-metrics", None) {
        Ok((404, _)) => {
            eprintln!("wag attack-sim: field not in -debug mode (no attack metrics). Start: waggled -debug");
            exit(1);
        }
        Err(e) => {
            eprintln!("wag attack-sim: {e}");
            exit(1);
        }
        _ => {}
    }
    let mut passed = 0;
    for s in &scenarios {
        let ok = match *s {
            "sybil" => sim_sybil(host),
            "taboo-grief" => sim_taboo_grief(host),
            "lease-squat" => sim_lease_squat(host),
            other => {
                eprintln!("wag attack-sim: unknown scenario '{other}'");
                exit(64);
            }
        };
        println!("  {:14} {}", s, if ok { "PASS" } else { "FAIL" });
        if ok {
            passed += 1;
        }
    }
    println!("  {:14} {}/{} defenses held", "TOTAL", passed, scenarios.len());
    exit(if passed == scenarios.len() { 0 } else { 1 });
}

fn reg(host: &str, id: &str) {
    let mut b = JsonObj::new();
    b.str("id", id);
    b.str("name", id);
    let _ = request(host, "POST", "/v1/agents", Some(&b.finish()));
}

fn dep(host: &str, agent: &str, resource: &str, kind: &str, intensity: f64, tier: &str) {
    let mut b = JsonObj::new();
    b.str("agent", agent);
    b.str("resource", resource);
    b.str("kind", kind);
    b.raw("intensity", &intensity.to_string());
    if !tier.is_empty() {
        b.str("evidence_tier", tier);
    }
    let _ = request(host, "POST", "/v1/signals", Some(&b.finish()));
}

fn metrics(host: &str) -> String {
    request(host, "GET", "/v1/debug/attack-metrics", None)
        .map(|(_, body)| body)
        .unwrap_or_default()
}

// Sybil: a ring of identities floods coordinated gold on a dead resource;
// the defense should surface it as a suspected cluster.
fn sim_sybil(host: &str) -> bool {
    let trap = "repo://dead-end-trap";
    for i in 0..8 {
        let a = format!("sybil-{i}");
        reg(host, &a);
        dep(host, &a, trap, "gold", 9.0, "");
    }
    let m = metrics(host);
    // the cluster report names the trap resource once a ring is detected
    let clusters = m.split("\"suspected_clusters\"").nth(1).unwrap_or("");
    clusters.contains(trap)
}

// Taboo griefing: an agent spams taboo to censor a legit path. Bare core does
// not authenticate taboo (that's Èṣù's job), so a pass = the flood is at least
// detected, quantifying the exposure the capability gate closes.
fn sim_taboo_grief(host: &str) -> bool {
    let path = "repo://legit-path";
    reg(host, "honest-worker");
    dep(host, "honest-worker", path, "gold", 8.0, "watch-derived");
    reg(host, "griefer");
    for _ in 0..12 {
        dep(host, "griefer", path, "taboo", 10.0, "");
    }
    let m = metrics(host);
    m.contains("griefer") || m.split("\"suspected_clusters\"").nth(1).unwrap_or("").contains("taboo")
}

// Lease squatting: claim and never release; expiry must reclaim within bound.
fn sim_lease_squat(host: &str) -> bool {
    reg(host, "squatter");
    let ttl = 2.0;
    for i in 0..4 {
        let mut b = JsonObj::new();
        b.str("agent", "squatter");
        b.str("resource", &format!("task://contested-{i}"));
        b.raw("ttl_s", &ttl.to_string());
        let _ = request(host, "POST", "/v1/claims", Some(&b.finish()));
    }
    reg(host, "honest-claimant");
    let mut b = JsonObj::new();
    b.str("agent", "honest-claimant");
    b.str("resource", "task://contested-0");
    b.raw("ttl_s", &ttl.to_string());
    let body = b.finish();
    // while squatted, honest claim must be denied (409)
    let denied = matches!(request(host, "POST", "/v1/claims", Some(&body)), Ok((409, _)));
    // after expiry, honest claim must succeed
    std::thread::sleep(std::time::Duration::from_secs_f64(ttl + 0.6));
    let granted = matches!(request(host, "POST", "/v1/claims", Some(&body)), Ok((200, _)));
    denied && granted
}

// ---- flag / arg parsing ------------------------------------------------------

fn parse(args: &[String]) -> (Vec<String>, Flags) {
    let mut pos = Vec::new();
    let mut flags = HashMap::new();
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        if let Some(name) = a.strip_prefix("--") {
            if i + 1 < args.len() && !args[i + 1].starts_with("--") {
                flags.insert(name.to_string(), args[i + 1].clone());
                i += 2;
            } else {
                flags.insert(name.to_string(), "true".to_string());
                i += 1;
            }
        } else {
            pos.push(a.clone());
            i += 1;
        }
    }
    (pos, flags)
}

fn one<'a>(pos: &'a [String], usage: &str) -> Result<[&'a String; 1], String> {
    match pos {
        [a, ..] => Ok([a]),
        _ => Err(format!("usage: wag {usage}")),
    }
}

fn two<'a>(pos: &'a [String], usage: &str) -> Result<[&'a String; 2], String> {
    match pos {
        [a, b, ..] => Ok([a, b]),
        _ => Err(format!("usage: wag {usage}")),
    }
}

fn num(v: &str, flag: &str) -> Result<String, String> {
    v.parse::<f64>()
        .map(|n| n.to_string())
        .map_err(|_| format!("{flag} expects a number, got '{v}'"))
}

fn query(flags: &Flags, keys: &[(&str, &str)]) -> String {
    let mut parts = Vec::new();
    for (flag, param) in keys {
        if let Some(v) = flags.get(*flag) {
            parts.push(format!("{param}={}", urlenc(v)));
        }
    }
    if parts.is_empty() {
        String::new()
    } else {
        format!("?{}", parts.join("&"))
    }
}

fn urlenc(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' | b'/' | b':' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

// ---- minimal JSON builder ----------------------------------------------------

struct JsonObj {
    parts: Vec<String>,
}

impl JsonObj {
    fn new() -> Self {
        JsonObj { parts: Vec::new() }
    }
    fn str(&mut self, k: &str, v: &str) {
        self.parts.push(format!("\"{}\":\"{}\"", esc(k), esc(v)));
    }
    fn raw(&mut self, k: &str, v: &str) {
        self.parts.push(format!("\"{}\":{}", esc(k), v));
    }
    fn str_array<'a>(&mut self, k: &str, vals: impl Iterator<Item = &'a str>) {
        let items: Vec<String> = vals.map(|v| format!("\"{}\"", esc(v))).collect();
        self.parts.push(format!("\"{}\":[{}]", esc(k), items.join(",")));
    }
    fn finish(self) -> String {
        format!("{{{}}}", self.parts.join(","))
    }
}

fn esc(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out
}

// ---- HTTP/1.1 over TcpStream ---------------------------------------------------

fn request(host: &str, method: &str, path: &str, body: Option<&str>) -> Out {
    let mut stream = TcpStream::connect(host).map_err(|e| format!("connect {host}: {e} (is waggled running?)"))?;
    let body_bytes = body.unwrap_or("");
    let req = format!(
        "{method} {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body_bytes}",
        body_bytes.len()
    );
    stream.write_all(req.as_bytes()).map_err(|e| e.to_string())?;

    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).map_err(|e| e.to_string())?;

    let split = find_headers_end(&raw).ok_or("malformed HTTP response")?;
    let head = String::from_utf8_lossy(&raw[..split]);
    let mut lines = head.lines();
    let status_line = lines.next().ok_or("empty response")?;
    let code: u16 = status_line
        .split_whitespace()
        .nth(1)
        .and_then(|s| s.parse().ok())
        .ok_or("bad status line")?;
    let chunked = lines.any(|l| {
        let l = l.to_ascii_lowercase();
        l.starts_with("transfer-encoding") && l.contains("chunked")
    });

    let body = &raw[split + 4..];
    let text = if chunked {
        String::from_utf8_lossy(&dechunk(body)?).into_owned()
    } else {
        String::from_utf8_lossy(body).into_owned()
    };
    Ok((code, text))
}

fn find_headers_end(raw: &[u8]) -> Option<usize> {
    raw.windows(4).position(|w| w == b"\r\n\r\n")
}

/// Minimal Transfer-Encoding: chunked decoder.
fn dechunk(mut b: &[u8]) -> Result<Vec<u8>, String> {
    let mut out = Vec::new();
    loop {
        let nl = b
            .windows(2)
            .position(|w| w == b"\r\n")
            .ok_or("truncated chunk header")?;
        let size_str = std::str::from_utf8(&b[..nl]).map_err(|e| e.to_string())?;
        let size = usize::from_str_radix(size_str.trim().split(';').next().unwrap_or("0"), 16)
            .map_err(|_| format!("bad chunk size '{size_str}'"))?;
        b = &b[nl + 2..];
        if size == 0 {
            return Ok(out);
        }
        if b.len() < size + 2 {
            return Err("truncated chunk body".into());
        }
        out.extend_from_slice(&b[..size]);
        b = &b[size + 2..];
    }
}
