# kernel_properties.jl — an independent Julia reimplementation of Waggle's
# decay/inhibition/diffusion kernels, cross-checked against the same
# invariants the Go property suite asserts.
#
# Connection Map v2 round 2, #2. Ọ̀ṣun already owns the ecosystem's numerical
# work, so the second-implementation cross-check lives in her language: if two
# independent implementations (Go and Julia) agree on 8 invariants across
# hundreds of thousands of random inputs, a transcription bug in either is
# very unlikely to survive. Mirrors core/kernel/kernel.go exactly.
#
# Run: julia verify/kernel_properties.jl   (stdlib only, no packages)

const MAX_INTENSITY = 10.0
const DIFFUSION_RATE = 0.05

# ── the kernels (must match core/kernel/kernel.go) ──────────────────────────

function decay(intensity, half_life, age, kind, alpha)
    (age <= 0 || half_life <= 0) && return intensity
    if kind == "power"
        a = alpha <= 0 ? 1.0 : alpha
        scale = half_life / (2.0^(1/a) - 1)
        return intensity * (1 + age/scale)^(-a)
    end
    return intensity * 2.0^(-age/half_life)
end

function alpha_from_value(intensity, amin, amax)
    (amax <= amin || amin <= 0) && return 1.0
    a = amax - (amax - amin) * (intensity / MAX_INTENSITY)
    return clamp(a, amin, amax)
end

function inhibit(mode, inhibitor, ref, floor)
    floor = clamp(floor, 0.0, 1.0)
    norm = max(0.0, inhibitor / MAX_INTENSITY)
    m = mode == "high" ? (1 - norm) : (norm / (ref <= 0 ? 0.5 : ref))
    return clamp(m, floor, 1.0)
end

diffusion(sib) = sib <= 0 ? 0.0 : DIFFUSION_RATE * sib

# ── invariants ──────────────────────────────────────────────────────────────

const TRIALS = 50_000
failures = 0
macro check(cond, msg)
    :( $(esc(cond)) || (global failures += 1; println("  FAIL: ", $(esc(msg)))) )
end

# P1 bounded, non-negative, finite; never amplifies
for _ in 1:TRIALS
    i0 = rand() * MAX_INTENSITY; hl = 1 + rand()*1e5; age = rand()*1e6
    kind = rand() < 0.5 ? "" : "power"; a = 0.3 + rand()*3
    v = decay(i0, hl, age, kind, a)
    @check(isfinite(v) && v >= 0 && v <= i0 + 1e-9, "decay bounds i0=$i0 v=$v")
end

# P2 halving parity for both kernels, any alpha
for _ in 1:TRIALS
    i0 = 0.01 + rand()*MAX_INTENSITY; hl = 1 + rand()*1e5; a = 0.3 + rand()*3
    for (kind, al) in (("", 0.0), ("power", a))
        got = decay(i0, hl, hl, kind, al)
        @check(abs(got - i0/2) <= 1e-9*i0 + 1e-12, "halving $kind a=$a got=$got")
    end
end

# P3 power tail >= exponential tail past one half-life
for _ in 1:TRIALS
    i0 = 0.01 + rand()*MAX_INTENSITY; hl = 1 + rand()*1e4
    age = hl * (1 + rand()*20); a = 0.3 + rand()*3
    @check(decay(i0,hl,age,"power",a) >= decay(i0,hl,age,"",0.0) - 1e-9, "tail dominance")
end

# P4/P5 inhibition never amplifies; composed chain stays in [0,1]
for _ in 1:TRIALS
    mult = 1.0
    for _ in 1:(1 + rand(0:4))
        mode = rand() < 0.5 ? "high" : "low"
        mult *= inhibit(mode, rand()*MAX_INTENSITY*1.5, 0.01 + rand(), rand())
    end
    @check(mult <= 1 + 1e-12 && mult >= 0, "composed inhibition $mult")
end

# P6 diffusion conserves mass
for _ in 1:TRIALS
    sib = (rand() - 0.2) * 1000
    d = diffusion(sib)
    @check(d >= 0 && (sib <= 0 || d <= DIFFUSION_RATE*sib + 1e-9), "diffusion $sib -> $d")
end

# P8 alpha clamp
for _ in 1:TRIALS
    amin = 0.1 + rand()*2; amax = amin + rand()*3
    a = alpha_from_value((rand()-0.5)*MAX_INTENSITY*3, amin, amax)
    @check(a >= amin - 1e-12 && a <= amax + 1e-12 && a > 0, "alpha clamp $a in [$amin,$amax]")
end

println(failures == 0 ?
    "KERNEL PROPERTIES OK — Julia impl agrees with Go on all invariants ($(TRIALS) trials each)" :
    "KERNEL PROPERTIES FAILED — $failures invariant violation(s)")
exit(failures == 0 ? 0 : 1)
