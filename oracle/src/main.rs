//! fractal-oracle — the ecosystem's shared Mandelbrot service.
//!
//! Promotes the Fractal Oracle out of Axiom's embedded Wasm species
//! (`Axiom/agents/oracle/src/lib.rs`) into a standalone HTTP service any
//! power can query: ỌṢỌVM's build-time perturbation gate, LOOM's strategy
//! robustness verdicts, Ṣàngó's anchoring, Vantage's cross-ecosystem
//! comparison — one implementation instead of N divergent copies. Axiom
//! keeps its embedded Wasm for offline/demo use; the determinism contract
//! below is what keeps the two byte-identical on the numeric payload.
//!
//! The iteration `z ← z² + c` (escape when |z|² > 4, checked before the
//! update step) is vendored verbatim from the embedded oracle. IEEE-754
//! f64 throughout, no randomness, no time dependence: anyone can re-verify
//! any result, which is what makes Zangbeto verification (replay) and
//! on-chain anchoring of robustness claims meaningful.
//!
//! Interface (see Agentic/docs/FRACTAL_ORACLE.md):
//!   GET  /.well-known/oracle.json     manifest
//!   GET  /v1/health                   {ok, version, scans, islands, uptime_s}
//!   GET  /v1/<tool>?...               per-tool conveniences (query params)
//!   POST /v1/invoke                   {"tool", "arg": {...}, "deposit": {...}}
//!
//! The shared depth convention: depth N → maxiter = 100·2^N, clamped to
//! [8, 2000] — the same "how hard should I look" dial as Waggle's
//! gradient?depth=N. A `deposit` block (or deposit_resource= on GET) makes
//! the service write its verdict to Waggle's bounded channel itself, at
//! evidence_tier watch-derived: the measuring instrument reporting, not the
//! interested party.

use std::collections::HashMap;
use std::env;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Instant;

static SCANS: AtomicU64 = AtomicU64::new(0);
static ISLANDS: AtomicU64 = AtomicU64::new(0);

const VERSION: &str = "0.1.0";

// ── Mandelbrot core (vendored from Axiom/agents/oracle — do not "improve") ──

/// Iterations before |z|>2, or `maxiter` if the orbit stays bounded.
fn escape_time(cr: f64, ci: f64, maxiter: u32) -> u32 {
    let mut zr = 0.0f64;
    let mut zi = 0.0f64;
    let mut i = 0u32;
    while i < maxiter {
        let zr2 = zr * zr;
        let zi2 = zi * zi;
        if zr2 + zi2 > 4.0 {
            return i;
        }
        zi = 2.0 * zr * zi + ci;
        zr = zr2 - zi2 + cr;
        i += 1;
    }
    maxiter
}

fn clamp_iter(m: i64) -> u32 {
    m.clamp(8, 2000) as u32
}

/// Shared depth convention: depth N → maxiter 100·2^N, clamped like every
/// other iteration budget. One mental model for "how deep should I look"
/// across scent gradients and fractal scans.
fn depth_to_maxiter(depth: i64) -> u32 {
    if depth < 0 {
        return clamp_iter(100);
    }
    clamp_iter(100i64.saturating_mul(1i64 << depth.min(6)))
}

/// Shared verdict for a single point: (escape, bounded, stability).
fn classify(cr: f64, ci: f64, maxiter: u32) -> (u32, bool, f64) {
    let e = escape_time(cr, ci, maxiter);
    let bounded = e >= maxiter;
    let stability = (e as f64) / (maxiter as f64); // 1.0 = fully bounded
    (e, bounded, stability)
}

fn verdict_str(bounded: bool, stability: f64) -> &'static str {
    if bounded {
        "robust island"
    } else if stability > 0.5 {
        "fragile boundary"
    } else {
        "escape zone"
    }
}

// ── Tools ───────────────────────────────────────────────────────────────────

struct ToolResult {
    json: String,
    /// stability score for the deposit write-back, when the tool has one
    stability: Option<f64>,
    escape: Option<u32>,
    maxiter: Option<u32>,
    verdict: Option<String>,
}

impl ToolResult {
    fn plain(json: String) -> Self {
        ToolResult { json, stability: None, escape: None, maxiter: None, verdict: None }
    }
}

fn tool_scan(a: &Args) -> ToolResult {
    SCANS.fetch_add(1, Ordering::SeqCst);
    let re0 = a.f("re0", -2.5);
    let re1 = a.f("re1", 1.0);
    let im0 = a.f("im0", -1.25);
    let im1 = a.f("im1", 1.25);
    let w = a.i("width", 96).clamp(1, 220);
    let h = a.i("height", 64).clamp(1, 160);
    let maxiter = a.maxiter(120);
    let mut esc = String::with_capacity((w * h * 4) as usize);
    let mut islands = 0u64;
    for yy in 0..h {
        let ci = im0 + (im1 - im0) * (yy as f64) / ((h - 1).max(1) as f64);
        for xx in 0..w {
            let cr = re0 + (re1 - re0) * (xx as f64) / ((w - 1).max(1) as f64);
            let e = escape_time(cr, ci, maxiter);
            if e >= maxiter {
                islands += 1;
            }
            if !(yy == 0 && xx == 0) {
                esc.push(',');
            }
            esc.push_str(&e.to_string());
        }
    }
    ISLANDS.fetch_add(islands, Ordering::SeqCst);
    ToolResult::plain(format!(
        "{{\"w\":{w},\"h\":{h},\"maxiter\":{maxiter},\"esc\":[{esc}]}}"
    ))
}

fn tool_escape_risk(a: &Args) -> ToolResult {
    SCANS.fetch_add(1, Ordering::SeqCst);
    let cr = a.f("re", 0.0);
    let ci = a.f("im", 0.0);
    let maxiter = a.maxiter(200);
    let (e, bounded, stability) = classify(cr, ci, maxiter);
    if bounded {
        ISLANDS.fetch_add(1, Ordering::SeqCst);
    }
    let verdict = verdict_str(bounded, stability);
    ToolResult {
        json: format!(
            "{{\"c\":[{},{}],\"escape\":{e},\"maxiter\":{maxiter},\"bounded\":{bounded},\"stability\":{},\"risk\":{},\"verdict\":\"{verdict}\"}}",
            fnum(cr), fnum(ci), fnum(stability), fnum(1.0 - stability)
        ),
        stability: Some(stability),
        escape: Some(e),
        maxiter: Some(maxiter),
        verdict: Some(verdict.to_string()),
    }
}

fn tool_robust_island(a: &Args) -> ToolResult {
    SCANS.fetch_add(1, Ordering::SeqCst);
    let cr = a.f("re", 0.0);
    let ci = a.f("im", 0.0);
    let maxiter = a.maxiter(300);
    let (e, bounded, stability) = classify(cr, ci, maxiter);
    if bounded {
        ISLANDS.fetch_add(1, Ordering::SeqCst);
    }
    ToolResult {
        json: format!(
            "{{\"bounded\":{bounded},\"depth\":{e},\"stability\":{},\"island\":{bounded}}}",
            fnum(stability)
        ),
        stability: Some(stability),
        escape: Some(e),
        maxiter: Some(maxiter),
        verdict: Some(verdict_str(bounded, stability).to_string()),
    }
}

fn fold_pairs(points: &[(f64, f64)], maxiter: u32) -> (i64, i64) {
    let mut bounded = 0i64;
    for &(cr, ci) in points {
        let (_, is_bounded, _) = classify(cr, ci, maxiter);
        if is_bounded {
            bounded += 1;
        }
    }
    (points.len() as i64, bounded)
}

fn tool_signal_filter(a: &Args) -> ToolResult {
    SCANS.fetch_add(1, Ordering::SeqCst);
    let (pairs, bounded) = fold_pairs(&a.points, a.maxiter(160));
    ISLANDS.fetch_add(bounded as u64, Ordering::SeqCst);
    let frac = if pairs > 0 { bounded as f64 / pairs as f64 } else { 0.0 };
    let signal = if pairs == 0 {
        "insufficient"
    } else if frac >= 0.6 {
        "accumulation"
    } else if frac >= 0.3 {
        "transition"
    } else {
        "breakout"
    };
    ToolResult {
        json: format!(
            "{{\"points\":{pairs},\"bounded\":{bounded},\"bounded_fraction\":{},\"signal\":\"{signal}\"}}",
            fnum(frac)
        ),
        stability: Some(frac),
        escape: None,
        maxiter: Some(a.maxiter(160)),
        verdict: Some(signal.to_string()),
    }
}

fn tool_swarm_stability(a: &Args) -> ToolResult {
    SCANS.fetch_add(1, Ordering::SeqCst);
    let (agents, bounded) = fold_pairs(&a.points, a.maxiter(200));
    ISLANDS.fetch_add(bounded as u64, Ordering::SeqCst);
    let stability = if agents > 0 { bounded as f64 / agents as f64 } else { 0.0 };
    let verdict = if agents == 0 {
        "no agents"
    } else if stability >= 0.66 {
        "stable attractor"
    } else if stability >= 0.33 {
        "approaching escape"
    } else {
        "chaotic divergence"
    };
    ToolResult {
        json: format!(
            "{{\"agents\":{agents},\"bounded\":{bounded},\"stability\":{},\"verdict\":\"{verdict}\"}}",
            fnum(stability)
        ),
        stability: Some(stability),
        escape: None,
        maxiter: Some(a.maxiter(200)),
        verdict: Some(verdict.to_string()),
    }
}

/// Tool arguments, from query params or the invoke body's "arg" object.
struct Args {
    nums: HashMap<String, f64>,
    points: Vec<(f64, f64)>,
}

impl Args {
    fn f(&self, k: &str, default: f64) -> f64 {
        self.nums.get(k).copied().unwrap_or(default)
    }
    fn i(&self, k: &str, default: i64) -> i64 {
        self.nums.get(k).map(|v| *v as i64).unwrap_or(default)
    }
    /// maxiter wins if given explicitly; else the shared depth convention;
    /// else the tool's default (clamped either way).
    fn maxiter(&self, default: i64) -> u32 {
        if let Some(m) = self.nums.get("maxiter") {
            return clamp_iter(*m as i64);
        }
        if let Some(d) = self.nums.get("depth") {
            return depth_to_maxiter(*d as i64);
        }
        clamp_iter(default)
    }
}

fn run_tool(tool: &str, a: &Args) -> Option<ToolResult> {
    match tool {
        "mandelbrot_scan" => Some(tool_scan(a)),
        "escape_time_risk" => Some(tool_escape_risk(a)),
        "robust_island_query" => Some(tool_robust_island(a)),
        "fractal_signal_filter" => Some(tool_signal_filter(a)),
        "swarm_stability_map" => Some(tool_swarm_stability(a)),
        _ => None,
    }
}

// ── Waggle write-back ────────────────────────────────────────────────────────

/// Deposit the verdict on Waggle's bounded channel: intensity 10·stability,
/// tier watch-derived (the instrument reporting, not the interested party).
/// The channel's registration supplies kernel, half-life and the
/// confidence-weighted alpha — the oracle just reports the score.
fn deposit_bounded(waggle: &str, agent: &str, resource: &str, r: &ToolResult) -> Result<String, String> {
    let stability = r.stability.ok_or("tool has no stability score to deposit")?;
    let mut meta = String::new();
    if let (Some(e), Some(m)) = (r.escape, r.maxiter) {
        meta.push_str(&format!("\"escape\":\"{e}\",\"maxiter\":\"{m}\","));
    } else if let Some(m) = r.maxiter {
        meta.push_str(&format!("\"maxiter\":\"{m}\","));
    }
    if let Some(v) = &r.verdict {
        meta.push_str(&format!("\"verdict\":\"{}\",", esc(v)));
    }
    meta.push_str("\"source\":\"fractal-oracle\"");
    let body = format!(
        "{{\"agent\":\"{}\",\"resource\":\"{}\",\"kind\":\"bounded\",\"intensity\":{},\"evidence_tier\":\"watch-derived\",\"meta\":{{{meta}}}}}",
        esc(agent), esc(resource), fnum(10.0 * stability)
    );
    let host = waggle.trim_start_matches("http://");
    let host = host.split('/').next().unwrap_or(host);
    http_post(host, "/v1/signals", &body)
}

fn http_post(host: &str, path: &str, body: &str) -> Result<String, String> {
    let mut stream = TcpStream::connect(host).map_err(|e| format!("connect {host}: {e}"))?;
    let req = format!(
        "POST {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    );
    stream.write_all(req.as_bytes()).map_err(|e| e.to_string())?;
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).map_err(|e| e.to_string())?;
    let split = raw
        .windows(4)
        .position(|w| w == b"\r\n\r\n")
        .ok_or("malformed HTTP response")?;
    Ok(String::from_utf8_lossy(&raw[split + 4..]).into_owned())
}

// ── Minimal JSON parsing (invoke bodies only) ───────────────────────────────

#[derive(Debug, Clone)]
enum Json {
    Null,
    // parsed for JSON completeness; no tool argument reads a bool yet
    #[allow(dead_code)]
    Bool(bool),
    Num(f64),
    Str(String),
    Arr(Vec<Json>),
    Obj(Vec<(String, Json)>),
}

impl Json {
    fn get(&self, key: &str) -> Option<&Json> {
        if let Json::Obj(kv) = self {
            kv.iter().find(|(k, _)| k == key).map(|(_, v)| v)
        } else {
            None
        }
    }
    fn as_str(&self) -> Option<&str> {
        if let Json::Str(s) = self {
            Some(s)
        } else {
            None
        }
    }
    fn as_f64(&self) -> Option<f64> {
        if let Json::Num(n) = self {
            Some(*n)
        } else {
            None
        }
    }
}

struct Parser<'a> {
    b: &'a [u8],
    i: usize,
}

impl<'a> Parser<'a> {
    fn parse(s: &'a str) -> Result<Json, String> {
        let mut p = Parser { b: s.as_bytes(), i: 0 };
        let v = p.value()?;
        p.ws();
        if p.i != p.b.len() {
            return Err("trailing JSON".into());
        }
        Ok(v)
    }
    fn ws(&mut self) {
        while self.i < self.b.len() && matches!(self.b[self.i], b' ' | b'\t' | b'\n' | b'\r') {
            self.i += 1;
        }
    }
    fn value(&mut self) -> Result<Json, String> {
        self.ws();
        match self.b.get(self.i) {
            Some(b'{') => self.obj(),
            Some(b'[') => self.arr(),
            Some(b'"') => Ok(Json::Str(self.string()?)),
            Some(b't') => self.lit("true", Json::Bool(true)),
            Some(b'f') => self.lit("false", Json::Bool(false)),
            Some(b'n') => self.lit("null", Json::Null),
            Some(_) => self.num(),
            None => Err("unexpected end of JSON".into()),
        }
    }
    fn lit(&mut self, word: &str, v: Json) -> Result<Json, String> {
        if self.b[self.i..].starts_with(word.as_bytes()) {
            self.i += word.len();
            Ok(v)
        } else {
            Err(format!("bad literal at {}", self.i))
        }
    }
    fn obj(&mut self) -> Result<Json, String> {
        self.i += 1; // {
        let mut kv = Vec::new();
        self.ws();
        if self.b.get(self.i) == Some(&b'}') {
            self.i += 1;
            return Ok(Json::Obj(kv));
        }
        loop {
            self.ws();
            let k = self.string()?;
            self.ws();
            if self.b.get(self.i) != Some(&b':') {
                return Err(format!("expected ':' at {}", self.i));
            }
            self.i += 1;
            let v = self.value()?;
            kv.push((k, v));
            self.ws();
            match self.b.get(self.i) {
                Some(b',') => self.i += 1,
                Some(b'}') => {
                    self.i += 1;
                    return Ok(Json::Obj(kv));
                }
                _ => return Err(format!("expected ',' or '}}' at {}", self.i)),
            }
        }
    }
    fn arr(&mut self) -> Result<Json, String> {
        self.i += 1; // [
        let mut items = Vec::new();
        self.ws();
        if self.b.get(self.i) == Some(&b']') {
            self.i += 1;
            return Ok(Json::Arr(items));
        }
        loop {
            items.push(self.value()?);
            self.ws();
            match self.b.get(self.i) {
                Some(b',') => self.i += 1,
                Some(b']') => {
                    self.i += 1;
                    return Ok(Json::Arr(items));
                }
                _ => return Err(format!("expected ',' or ']' at {}", self.i)),
            }
        }
    }
    fn string(&mut self) -> Result<String, String> {
        if self.b.get(self.i) != Some(&b'"') {
            return Err(format!("expected string at {}", self.i));
        }
        self.i += 1;
        let mut out = String::new();
        while let Some(&c) = self.b.get(self.i) {
            self.i += 1;
            match c {
                b'"' => return Ok(out),
                b'\\' => {
                    let e = *self.b.get(self.i).ok_or("truncated escape")?;
                    self.i += 1;
                    match e {
                        b'"' => out.push('"'),
                        b'\\' => out.push('\\'),
                        b'/' => out.push('/'),
                        b'n' => out.push('\n'),
                        b't' => out.push('\t'),
                        b'r' => out.push('\r'),
                        b'b' => out.push('\u{8}'),
                        b'f' => out.push('\u{c}'),
                        b'u' => {
                            let hex = self
                                .b
                                .get(self.i..self.i + 4)
                                .ok_or("truncated \\u escape")?;
                            self.i += 4;
                            let code = u32::from_str_radix(
                                std::str::from_utf8(hex).map_err(|e| e.to_string())?,
                                16,
                            )
                            .map_err(|e| e.to_string())?;
                            out.push(char::from_u32(code).unwrap_or('\u{fffd}'));
                        }
                        _ => return Err("bad escape".into()),
                    }
                }
                _ => {
                    // collect the raw UTF-8 byte run
                    let start = self.i - 1;
                    while let Some(&n) = self.b.get(self.i) {
                        if n == b'"' || n == b'\\' {
                            break;
                        }
                        self.i += 1;
                    }
                    out.push_str(
                        std::str::from_utf8(&self.b[start..self.i]).map_err(|e| e.to_string())?,
                    );
                }
            }
        }
        Err("unterminated string".into())
    }
    fn num(&mut self) -> Result<Json, String> {
        let start = self.i;
        while let Some(&c) = self.b.get(self.i) {
            if c.is_ascii_digit() || matches!(c, b'-' | b'+' | b'.' | b'e' | b'E') {
                self.i += 1;
            } else {
                break;
            }
        }
        std::str::from_utf8(&self.b[start..self.i])
            .ok()
            .and_then(|s| s.parse::<f64>().ok())
            .map(Json::Num)
            .ok_or_else(|| format!("bad number at {start}"))
    }
}

/// Build tool Args from a parsed "arg" object: numeric fields go to nums,
/// "points" ([[re,im],...] or flat [re,im,re,im,...]) to points.
fn args_from_json(arg: &Json) -> Args {
    let mut nums = HashMap::new();
    let mut points = Vec::new();
    if let Json::Obj(kv) = arg {
        for (k, v) in kv {
            match (k.as_str(), v) {
                ("points", Json::Arr(items)) => {
                    let mut flat: Vec<f64> = Vec::new();
                    for it in items {
                        match it {
                            Json::Arr(pair) => {
                                if let (Some(a), Some(b)) =
                                    (pair.first().and_then(Json::as_f64), pair.get(1).and_then(Json::as_f64))
                                {
                                    points.push((a, b));
                                }
                            }
                            Json::Num(n) => flat.push(*n),
                            _ => {}
                        }
                    }
                    for pair in flat.chunks_exact(2) {
                        points.push((pair[0], pair[1]));
                    }
                }
                (_, Json::Num(n)) => {
                    nums.insert(k.clone(), *n);
                }
                _ => {}
            }
        }
    }
    Args { nums, points }
}

/// Build tool Args from query params; points come as a comma-separated list.
fn args_from_query(query: &str) -> Args {
    let mut nums = HashMap::new();
    let mut points = Vec::new();
    for pair in query.split('&') {
        let (k, v) = match pair.split_once('=') {
            Some(kv) => kv,
            None => continue,
        };
        if k == "points" {
            let flat: Vec<f64> = v
                .split(',')
                .filter_map(|s| s.trim().parse::<f64>().ok())
                .collect();
            for p in flat.chunks_exact(2) {
                points.push((p[0], p[1]));
            }
        } else if let Ok(n) = v.parse::<f64>() {
            nums.insert(k.to_string(), n);
        }
    }
    Args { nums, points }
}

// ── HTTP server ──────────────────────────────────────────────────────────────

fn manifest() -> String {
    format!(
        r#"{{"service":"fractal-oracle","version":"{VERSION}","protocol":"fractal-oracle/v1",
"description":"The ecosystem's shared Mandelbrot service: z <- z^2 + c escape-time analysis as robustness verdicts. Deterministic (IEEE-754 f64, escape |z|^2 > 4 checked before the update step, row-major grids) so any party can re-verify any result — the property Zangbeto replay verification and on-chain anchoring depend on.",
"depth_convention":"depth N -> maxiter = 100*2^N, clamped to [8,2000]; same dial as Waggle gradient?depth=N",
"deposit":"pass deposit_resource= (GET) or a deposit:{{resource,waggle?,agent?}} block (invoke) and the oracle writes the verdict to Waggle's bounded channel at evidence_tier watch-derived: intensity 10*stability, meta {{escape,maxiter,verdict}}",
"tools":[
{{"name":"mandelbrot_scan","params":"re0,re1,im0,im1,width<=220,height<=160,depth|maxiter","returns":"{{w,h,maxiter,esc:[...]}} row-major"}},
{{"name":"escape_time_risk","params":"re,im,depth|maxiter","returns":"{{c,escape,maxiter,bounded,stability,risk,verdict}}"}},
{{"name":"robust_island_query","params":"re,im,depth|maxiter","returns":"{{bounded,depth,stability,island}}"}},
{{"name":"fractal_signal_filter","params":"points (pairs),depth|maxiter","returns":"{{points,bounded,bounded_fraction,signal}}"}},
{{"name":"swarm_stability_map","params":"points (pairs),depth|maxiter","returns":"{{agents,bounded,stability,verdict}} — feed Yemoja spawn-throttling"}}
]}}"#
    )
}

fn handle(mut stream: TcpStream, start: Instant, waggle_default: &str) {
    let mut buf = Vec::new();
    let mut chunk = [0u8; 4096];
    // read until end of headers, then until Content-Length is satisfied
    let (head_end, header_text) = loop {
        match stream.read(&mut chunk) {
            Ok(0) => return,
            Ok(n) => buf.extend_from_slice(&chunk[..n]),
            Err(_) => return,
        }
        if let Some(pos) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
            break (pos + 4, String::from_utf8_lossy(&buf[..pos]).into_owned());
        }
        if buf.len() > 1 << 20 {
            return;
        }
    };
    let content_length: usize = header_text
        .lines()
        .find_map(|l| {
            let l = l.to_ascii_lowercase();
            l.strip_prefix("content-length:").map(|v| v.trim().parse().unwrap_or(0))
        })
        .unwrap_or(0);
    while buf.len() < head_end + content_length {
        match stream.read(&mut chunk) {
            Ok(0) => break,
            Ok(n) => buf.extend_from_slice(&chunk[..n]),
            Err(_) => return,
        }
    }
    let body = String::from_utf8_lossy(&buf[head_end..]).into_owned();

    let request_line = header_text.lines().next().unwrap_or("");
    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or("");
    let target = parts.next().unwrap_or("/");
    let (path, query) = target.split_once('?').unwrap_or((target, ""));

    let (code, payload) = route(method, path, query, &body, start, waggle_default);
    let resp = format!(
        "HTTP/1.1 {code} {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{payload}",
        if code == 200 { "OK" } else { "Error" },
        payload.len()
    );
    let _ = stream.write_all(resp.as_bytes());
}

fn route(
    method: &str,
    path: &str,
    query: &str,
    body: &str,
    start: Instant,
    waggle_default: &str,
) -> (u16, String) {
    match (method, path) {
        ("GET", "/.well-known/oracle.json") => (200, manifest()),
        ("GET", "/v1/health") => (
            200,
            format!(
                "{{\"ok\":true,\"version\":\"{VERSION}\",\"scans\":{},\"islands\":{},\"uptime_s\":{}}}",
                SCANS.load(Ordering::SeqCst),
                ISLANDS.load(Ordering::SeqCst),
                fnum(start.elapsed().as_secs_f64())
            ),
        ),
        ("POST", "/v1/invoke") => {
            let parsed = match Parser::parse(body) {
                Ok(v) => v,
                Err(e) => return (400, format!("{{\"error\":\"invalid JSON: {}\"}}", esc(&e))),
            };
            let tool = match parsed.get("tool").and_then(Json::as_str) {
                Some(t) => t.to_string(),
                None => return (400, "{\"error\":\"tool is required\"}".into()),
            };
            let args = args_from_json(parsed.get("arg").unwrap_or(&Json::Null));
            let result = match run_tool(&tool, &args) {
                Some(r) => r,
                None => return (404, format!("{{\"error\":\"unknown tool: {}\"}}", esc(&tool))),
            };
            let mut deposit_note = String::new();
            if let Some(dep) = parsed.get("deposit") {
                let resource = dep.get("resource").and_then(Json::as_str).unwrap_or("");
                if resource.is_empty() {
                    return (400, "{\"error\":\"deposit.resource is required\"}".into());
                }
                let waggle = dep
                    .get("waggle")
                    .and_then(Json::as_str)
                    .unwrap_or(waggle_default);
                let agent = dep
                    .get("agent")
                    .and_then(Json::as_str)
                    .unwrap_or("fractal-oracle");
                deposit_note = match deposit_bounded(waggle, agent, resource, &result) {
                    Ok(_) => ",\"deposited\":true".into(),
                    Err(e) => format!(",\"deposited\":false,\"deposit_error\":\"{}\"", esc(&e)),
                };
            }
            (
                200,
                format!(
                    "{{\"tool\":\"{}\",\"result\":{}{deposit_note},\"oracle\":{{\"version\":\"{VERSION}\",\"scans\":{}}}}}",
                    esc(&tool),
                    result.json,
                    SCANS.load(Ordering::SeqCst)
                ),
            )
        }
        ("GET", _) if path.starts_with("/v1/") => {
            let tool = &path[4..];
            let args = args_from_query(query);
            match run_tool(tool, &args) {
                Some(r) => {
                    // deposit_resource= makes the GET path write back too
                    let dep = query
                        .split('&')
                        .find_map(|p| p.strip_prefix("deposit_resource="))
                        .map(urldecode);
                    if let Some(resource) = dep {
                        let _ = deposit_bounded(waggle_default, "fractal-oracle", &resource, &r);
                    }
                    (200, r.json)
                }
                None => (404, format!("{{\"error\":\"unknown tool: {}\"}}", esc(tool))),
            }
        }
        _ => (404, "{\"error\":\"not found\"}".into()),
    }
}

// ── helpers ──────────────────────────────────────────────────────────────────

fn fnum(v: f64) -> String {
    if v == v.trunc() && v.abs() < 1e15 {
        format!("{}", v as i64)
    } else {
        format!("{v}")
    }
}

fn esc(s: &str) -> String {
    s.chars()
        .flat_map(|c| match c {
            '"' => "\\\"".chars().collect::<Vec<_>>(),
            '\\' => "\\\\".chars().collect(),
            '\n' => "\\n".chars().collect(),
            c => vec![c],
        })
        .collect()
}

fn urldecode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' && i + 2 < b.len() {
            if let Ok(v) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(if b[i] == b'+' { b' ' } else { b[i] });
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn main() {
    let mut addr = "127.0.0.1:7778".to_string();
    let mut args = env::args().skip(1);
    while let Some(a) = args.next() {
        if a == "-addr" {
            if let Some(v) = args.next() {
                addr = v;
            }
        }
    }
    let waggle = env::var("WAGGLE_URL").unwrap_or_else(|_| "127.0.0.1:7777".into());
    let start = Instant::now();
    let listener = TcpListener::bind(&addr).unwrap_or_else(|e| {
        eprintln!("fractal-oracle: bind {addr}: {e}");
        std::process::exit(1);
    });
    println!("fractal-oracle: listening on {addr} (manifest at /.well-known/oracle.json), waggle write-back to {waggle}");
    for conn in listener.incoming().flatten() {
        let waggle = waggle.clone();
        std::thread::spawn(move || handle(conn, start, &waggle));
    }
}

// ── determinism fixtures ─────────────────────────────────────────────────────
// These values must match Axiom's embedded Wasm oracle byte-for-byte on the
// numeric payload; a Zangbeto verification of an oracle receipt is a replay
// against exactly these semantics.

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn origin_is_bounded() {
        assert_eq!(escape_time(0.0, 0.0, 500), 500);
        let (_, bounded, stability) = classify(0.0, 0.0, 500);
        assert!(bounded);
        assert_eq!(stability, 1.0);
    }

    #[test]
    fn far_point_escapes_immediately() {
        // z0=0 survives the first check; z1=c has |c|^2=8>4 → escape at i=1
        assert_eq!(escape_time(2.0, 2.0, 100), 1);
    }

    #[test]
    fn period_two_bulb_is_bounded() {
        assert_eq!(escape_time(-1.0, 0.0, 2000), 2000);
    }

    #[test]
    fn just_outside_cardioid_escapes() {
        let e = escape_time(0.26, 0.0, 1000);
        assert!(e < 1000, "0.26 must escape, got {e}");
    }

    #[test]
    fn depth_convention() {
        assert_eq!(depth_to_maxiter(0), 100);
        assert_eq!(depth_to_maxiter(1), 200);
        assert_eq!(depth_to_maxiter(4), 1600);
        assert_eq!(depth_to_maxiter(5), 2000); // clamped
        assert_eq!(depth_to_maxiter(60), 2000);
    }

    #[test]
    fn verdict_thresholds_match_embedded_oracle() {
        assert_eq!(verdict_str(true, 1.0), "robust island");
        assert_eq!(verdict_str(false, 0.6), "fragile boundary");
        assert_eq!(verdict_str(false, 0.2), "escape zone");
    }

    #[test]
    fn invoke_json_roundtrip() {
        let v = Parser::parse(
            r#"{"tool":"swarm_stability_map","arg":{"points":[[0,0],[-1,0],[2,2]],"depth":1}}"#,
        )
        .unwrap();
        let args = args_from_json(v.get("arg").unwrap());
        assert_eq!(args.points.len(), 3);
        let r = tool_swarm_stability(&args);
        assert!(r.json.contains("\"agents\":3"));
        assert!(r.json.contains("\"bounded\":2"));
        assert!(r.json.contains("stable attractor"));
    }

    #[test]
    fn scan_grid_shape() {
        let mut nums = HashMap::new();
        nums.insert("width".into(), 4.0);
        nums.insert("height".into(), 3.0);
        let r = tool_scan(&Args { nums, points: vec![] });
        let esc_count = r.json.split("\"esc\":[").nth(1).unwrap().trim_end_matches("]}").split(',').count();
        assert_eq!(esc_count, 12);
    }
}
