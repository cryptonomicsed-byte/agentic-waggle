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
  sniff                 read the field
                          --resource URI | --prefix URI [--kind K] [--min N] [--limit N]
  gradient              ranked hotspots: where is the swarm's attention?
                          [--prefix URI] [--kind K] [--k N]
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
        "sniff" => get(&host, &format!("/v1/sniff{}", query(&flags, &[("resource", "resource"), ("prefix", "prefix"), ("kind", "kind"), ("agent", "agent"), ("min", "min"), ("limit", "limit")]))),
        "gradient" => get(&host, &format!("/v1/gradient{}", query(&flags, &[("prefix", "prefix"), ("kind", "kind"), ("k", "k")]))),
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
    if let Some(n) = flags.get("note") {
        body.str("note", n);
    }
    request(host, "POST", "/v1/signals", Some(&body.finish()))
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
