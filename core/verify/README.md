# Kernel verification

The Waggle field's math is small, pure, and safety-relevant, so it is verified
on three levels — from cheapest/broadest to most rigorous/narrowest.

| Level | Where | Covers | Status |
|-------|-------|--------|--------|
| Unit | `core/field_test.go`, `core/channels_test.go` | Named cases through the live daemon | runs in `go test ./...` |
| Property | `core/kernel/kernel_property_test.go` | 8 invariants × 20k randomized inputs each, over the whole pure kernel | runs in `go test ./...` |
| Formal | `core/verify/WaggleKernel.lean` | The two decay kernels halve at exactly one half-life; age-0 identity; non-negativity | Lean 4 + Mathlib (statements authoritative; proofs port to a Mathlib revision) |
| Numerical harness | `core/verify/kernel_properties.jl` | Same invariants as the property tests, cross-checked in Julia (Ọ̀ṣun's numeric muscle) so a second independent implementation agrees | needs a Julia runtime |

## The invariants (why they matter)

Everything from round 2 rests on the field's math not running away once channels
*interact* instead of decaying independently. The invariants that guarantee it:

1. **Bounded, non-negative decay** — an intensity that starts in range never
   goes negative, non-finite, or above its initial value. (`P1`)
2. **Halving parity** — both kernels equal `i/2` at one half-life, for every
   `alpha`, so `half_life_s` means one thing everywhere. (`P2`, Lean)
3. **Power tail dominates** — past one half-life the heavy tail is always ≥ the
   exponential, the property that makes it right for durable findings. (`P3`)
4. **Inhibition never amplifies** — cross-inhibition is always a multiplier in
   `[floor, 1]`. This is the runaway-feedback guard: no taboo→gold→bounded
   interaction chain can push a reading *above* its own un-inhibited value.
   (`P4`, `P5`)
5. **Diffusion conserves mass** — the sibling bleed is at most `DiffusionRate`
   of the surrounding mass and never negative; a hot neighborhood can warm a
   cold resource but cannot manufacture signal from nothing. (`P6`)
6. **Effective monotone in tier** — trust promotion never makes a signal read
   weaker. (`P7`)
7. **Alpha stays in range** — the confidence-weighted exponent is clamped, so
   `Decay` can never receive an alpha that makes it diverge. (`P8`)

Together 4+5 answer the specific concern raised for this round: *does taboo's
cross-inhibition, feeding back through gold's decay, ever compensate
incorrectly and amplify?* No — every path signals can influence each other
through is a product of `[0,1]` multipliers on independently-decaying values,
so the composed system is bounded by construction.

## Running

```bash
cd core && go test ./kernel/           # property suite (fast, ~0.02s)
lake env lean verify/WaggleKernel.lean # formal proof, if Lean+Mathlib present
julia verify/kernel_properties.jl      # numeric cross-check, if Julia present
```
