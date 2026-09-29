# Identity-Boundary Results (Phase 9)

Source: `boundary_test.go`. The resolver takes dependency classification as
input; these tests quantify what honest classification buys and what mistakes
cost. No workload-specific resolver branches exist (one code path serves W1
and W2; asserted behaviorally).

| # | Mistake | Result |
|---|---|---|
| 1 | Generation dep classified verify-only | VERIFY reuses divergent bytes, verdicts agree — UNDETECTED (stop-signal datum) |
| 2 | Validation-only dep classified comp-side | RECOMPUTE (over-conservative, verdicts agree); waste quantified (~119µs here) |
| 3 | Verifier hidden state | Invisible by construction; resolution deterministic on declared inputs |
| 4 | Nondeterministic generator | Publish conflict detects (same key, distinct bytes refuses) |
| 5 | Env omitted, affects output | Same-key re-publication conflicts (detected, fail-closed) |
| 6 | Verify hidden env | Deterministic on declared inputs; second call progresses VERIFY→REUSE |
| 7 | Genericity | W1 + W2 VERIFY correct through identical code path |

Stop-signal assessment: safe VERIFY depends on honest classification AND on
verifiers that re-derive the properties they attest (W1-style source-grounded
checks catch content divergence; byte-only checks do not). The primitive is
justified where producers declare generation inputs honestly — the common,
auditable case — but scenario 1 bounds it: misclassified generation deps are
a silent-correctness hole no resolver logic can close without re-derivation.
Not a stop (deterministic producers + declared inputs are the norm in the
target regime), but a documented precondition, not a patch.
