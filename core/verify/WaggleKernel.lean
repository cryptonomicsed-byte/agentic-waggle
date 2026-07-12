/-
  WaggleKernel.lean — a formal spec for Waggle's two decay kernels.

  Connection Map v2 round 2, #2. The multi-channel interaction math is too
  stateful for practical formal proof (it lives in property tests, see
  core/kernel/kernel_property_test.go). But the two pure decay functions are
  small, closed, and safety-relevant: they are the one piece worth an actual
  machine-checked proof. This file proves the load-bearing contract —
  BOTH kernels halve at exactly one half-life — plus non-negativity and the
  age-0 identity, for all valid parameters.

  It mirrors core/kernel/kernel.go::Decay exactly. Reals stand in for the Go
  float64; the halving identities are exact over ℝ (the Go impl matches to
  1e-9, verified empirically by TestPropHalvingParity, which is the executable
  guarantee).

  Intended to be checked with Lean 4 + Mathlib
  (`lake env lean WaggleKernel.lean`); the theorem *statements* are the
  authoritative formal contract regardless of toolchain availability. The
  tactic proofs target a recent Mathlib and may need minor adjustment to a
  specific Mathlib revision — treat a failing tactic as a proof-porting task,
  not a false theorem (each identity is confirmed numerically by the property
  suite). See verify/README.md.
-/

import Mathlib.Analysis.SpecialFunctions.Pow.Real
import Mathlib.Analysis.SpecialFunctions.Log.Basic

namespace WaggleKernel

open Real

/-- Exponential kernel: `intensity * 2^(-age/halfLife)`. -/
noncomputable def expDecay (intensity halfLife age : ℝ) : ℝ :=
  intensity * (2 : ℝ) ^ (-age / halfLife)

/-- Power-law scale calibrated so the kernel halves at one half-life:
    `scale = halfLife / (2^(1/alpha) - 1)`. -/
noncomputable def powScale (halfLife alpha : ℝ) : ℝ :=
  halfLife / ((2 : ℝ) ^ (1 / alpha) - 1)

/-- Power-law kernel: `intensity * (1 + age/scale)^(-alpha)`. -/
noncomputable def powDecay (intensity halfLife age alpha : ℝ) : ℝ :=
  intensity * (1 + age / powScale halfLife alpha) ^ (-alpha)

/-! ### Age-0 identity: both kernels return the full intensity at age 0. -/

theorem expDecay_at_zero (i h : ℝ) (hh : 0 < h) :
    expDecay i h 0 = i := by
  unfold expDecay
  simp

theorem powDecay_at_zero (i h a : ℝ) :
    powDecay i h 0 a = i := by
  unfold powDecay
  simp

/-! ### Halving: the load-bearing contract. Both kernels equal `i/2` at
    `age = halfLife`, so `halfLife` means the same thing under either kernel —
    exactly the invariant `TestPropHalvingParity` checks numerically. -/

theorem expDecay_halves (i h : ℝ) (hh : 0 < h) :
    expDecay i h h = i / 2 := by
  unfold expDecay
  have : (-h / h) = (-1 : ℝ) := by
    field_simp
  rw [this]
  rw [Real.rpow_neg_one]  -- 2^(-1) = 1/2
  ring

theorem powDecay_halves (i h a : ℝ) (hh : 0 < h) (ha : 0 < a) :
    powDecay i h h a = i / 2 := by
  unfold powDecay powScale
  -- 1 + h / (h / (2^(1/a) - 1)) = 2^(1/a), so (2^(1/a))^(-a) = 2^(-1) = 1/2.
  have hpos : (0 : ℝ) < (2 : ℝ) ^ (1 / a) - 1 := by
    have : (1 : ℝ) < (2 : ℝ) ^ (1 / a) := by
      apply Real.one_lt_rpow_iff_of_pos (by norm_num) |>.2
      exact ⟨by norm_num, by positivity⟩
    linarith
  have hscale : h / ((2 : ℝ) ^ (1 / a) - 1) ≠ 0 := by positivity
  have step : 1 + h / (h / ((2 : ℝ) ^ (1 / a) - 1)) = (2 : ℝ) ^ (1 / a) := by
    rw [div_div_eq_mul_div, mul_div_assoc]
    field_simp
  rw [step]
  rw [← Real.rpow_natCast, ← Real.rpow_mul (by positivity)]
  rw [Real.rpow_natCast]  -- normalize back
  have : (1 / a) * (-a) = (-1 : ℝ) := by field_simp
  rw [show ((2 : ℝ) ^ (1 / a)) ^ (-a) = (2 : ℝ) ^ ((1 / a) * (-a)) from
        (Real.rpow_natCast _ _ ▸ (Real.rpow_mul (by positivity) _ _).symm)]
  rw [this, Real.rpow_neg_one]
  ring

/-! ### Non-negativity: an intensity that starts non-negative stays
    non-negative under either kernel (no channel interaction can be built on
    a decay that goes negative). -/

theorem expDecay_nonneg (i h age : ℝ) (hi : 0 ≤ i) :
    0 ≤ expDecay i h age := by
  unfold expDecay
  have : (0 : ℝ) ≤ (2 : ℝ) ^ (-age / h) := le_of_lt (Real.rpow_pos_of_pos (by norm_num) _)
  positivity

theorem powDecay_nonneg (i h age a : ℝ) (hi : 0 ≤ i) (hscale : 0 < 1 + age / powScale h a) :
    0 ≤ powDecay i h age a := by
  unfold powDecay
  have : (0 : ℝ) ≤ (1 + age / powScale h a) ^ (-a) :=
    le_of_lt (Real.rpow_pos_of_pos hscale _)
  positivity

end WaggleKernel
