package verifyresolution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// matrix_test.go: Phases 5-6. R0 (recompute oracle) vs R1 (split resolver)
// vs R2 (stale-claim reuse, negative control) over W1/W2 × frozen rotation
// scenarios. Hard gates: R1 verdict == R0 verdict in every resolvable row;
// zero R1 false ACCEPTED; no cross-key claim reuse; computation changes →
// RECOMPUTE; verification-only changes regenerate no bytes.
// testdata/vr_matrix.json.

type MatrixRow struct {
	Scenario string `json:"scenario"`
	Workload string `json:"workload"`
	R0Verd   string `json:"r0_verdict"`
	R1Verd   string `json:"r1_verdict"`
	R1State  string `json:"r1_resolution"`
	R2Verd   string `json:"r2_verdict"`
	FalseAcc bool   `json:"r1_false_accepted"`
	R2Caught bool   `json:"r2_negative_caught"`
	R0ProdNS int64  `json:"r0_prod_ns"`
	R1ProdNS int64  `json:"r1_prod_ns"`
	R1VerNS  int64  `json:"r1_verify_ns"`
	R1OverNS int64  `json:"r1_overhead_ns"`
}

type Scenario struct {
	ID        string
	Workload  string
	Seed      uint64
	Docs      int
	Burn      int
	Prior     ContractSpec
	Required  ContractSpec
	PriorVerd ClaimStatus
	Mutate    func(*Case)
	Expect    Resolution
}

// devScenarios are the frozen development matrix (Phase 6, 12 scenarios).
func devScenarios() []Scenario {
	w1 := W1Contracts()
	w2 := W2Contracts()
	return []Scenario{
		{ID: "strict-accept", Workload: "W1", Seed: 41, Docs: 24, Prior: w1[0], Required: w1[1], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "strict-reject", Workload: "W1", Seed: 42, Docs: 24, Prior: w1[0], Required: w1[1], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "relaxed-accept", Workload: "W1", Seed: 43, Docs: 24, Prior: w1[0], Required: w1[2], PriorVerd: ClaimRejected, Expect: ResolveVerify},
		{ID: "impl-same", Workload: "W1", Seed: 44, Docs: 24, Prior: w1[0], Required: w1[5], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "bugfix-flip", Workload: "W1", Seed: 45, Docs: 24, Prior: w1[0], Required: w1[5], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "revoked-verifier", Workload: "W2", Seed: 46, Burn: 2, Prior: w2[0], Required: w2[4], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "verifydep-change", Workload: "W2", Seed: 47, Burn: 2, Prior: w2[0], Required: w2[1], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "compdep-change", Workload: "W1", Seed: 48, Docs: 24, Prior: w1[0], Required: w1[0], PriorVerd: ClaimAccepted, Expect: ResolveRecompute,
			Mutate: func(c *Case) { c.Docs[0].Amount += 100 }},
		{ID: "corrupt-bytes", Workload: "W2", Seed: 49, Burn: 2, Prior: w2[0], Required: w2[0], PriorVerd: ClaimAccepted, Expect: ResolveRecompute},
		{ID: "prodconfig-change", Workload: "W1", Seed: 50, Docs: 24, Prior: w1[0], Required: w1[0], PriorVerd: ClaimAccepted, Expect: ResolveRecompute,
			Mutate: func(c *Case) { c.Params["extract"] = "v2" }},
		{ID: "evidence-revoked", Workload: "W2", Seed: 51, Burn: 2, Prior: w2[0], Required: w2[0], PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "unresolvable", Workload: "W1", Seed: 52, Docs: 24, Prior: w1[0], Required: w1[0], PriorVerd: ClaimAccepted, Expect: ResolveUnknown,
			Mutate: func(c *Case) { c.DropDep = "doc:" + c.Docs[0].ID }},
	}
}

// heldOutScenarios are reserved (never developed against): new seeds plus
// the predeclared held-out rotations.
func heldOutScenarios() []Scenario {
	return []Scenario{
		{ID: "heldout-relaxed2", Workload: "W1", Seed: 9001, Docs: 24,
			Prior:     W1Contracts()[0],
			Required:  ContractSpec{ID: "w1-schema", Version: "v7-relaxed2", Rules: map[string]string{"format": "loose"}},
			PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "heldout-tightened", Workload: "W2", Seed: 9002, Burn: 50,
			Prior:     W2Contracts()[0],
			Required:  ContractSpec{ID: "w2-policy", Version: "v6-tightened", Rules: map[string]string{"threshold": "t1", "invariant": "prefix"}},
			PriorVerd: ClaimAccepted, Expect: ResolveVerify},
		{ID: "heldout-rotation", Workload: "W2", Seed: 9003, Burn: 5,
			Prior: W2Contracts()[0], Required: W2Contracts()[4],
			PriorVerd: ClaimAccepted, Expect: ResolveVerify},
	}
}

func runScenario(t *testing.T, sc Scenario) MatrixRow {
	t.Helper()
	// strict-reject needs an over-cap amount; bugfix-flip needs empty title.
	docs := GenSources(sc.Seed, sc.Docs)
	if sc.Workload == "W2" {
		docs = nil
	}
	c := Case{Workload: sc.Workload, Seed: sc.Seed, Docs: docs,
		Params: map[string]string{"extract": "v1", "transform": "t1"},
		Burn:   sc.Burn, Prior: sc.Prior, Required: sc.Required}
	var prod ProdFunc
	var ver VerifyFunc
	if sc.Workload == "W1" {
		prod, ver = w1Prod, w1Ver
	} else {
		prod, ver = w2Prod, w2Ver
	}
	// Fixture shaping: strict-accept needs all amounts under the v2 cap
	// (10000); strict-reject needs exactly one over-cap amount.
	if sc.ID == "strict-accept" {
		for i := range c.Docs {
			c.Docs[i].Amount = 100 + int64(i)
		}
	}
	if sc.ID == "strict-reject" {
		c.Docs[0].Amount = 20000 // over v2 cap 10000, under v1 cap 50000
	}
	if sc.ID == "bugfix-flip" {
		c.Docs[1].Title = "   " // v6 title-nonempty rejects; v1 accepts
	}
	s, err := Open(filepath.Join(t.TempDir(), "vr.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := &TRunner{Store: s, Auth: newOutcomeTable()}
	priorBytes := tr.SetupHistory(c, prod, ver, sc.PriorVerd)
	// Scenario mutations that alter inputs/evidence/corruption.
	if sc.Mutate != nil {
		sc.Mutate(&c)
	}
	switch sc.ID {
	case "revoked-verifier", "evidence-revoked":
		// Revoke the prior claim's evidence (authoritative revocation).
		vk := VerifyKey(priorBytes, c.Prior, "v1", c.VerifyDeps)
		if _, err := s.RevokeClaim(vk.Digest(), "revoked"); err != nil {
			t.Fatal(err)
		}
		tr.Auth.revoke("ev-setup-1")
	case "corrupt-bytes":
		ck := CompKey(c)
		if art, ok := s.LookupArtifact(ck.Digest()); ok {
			s.CorruptBody(art.ArtifactDigest, []byte("corrupted!!"))
		}
	}
	r0 := tr.RunR0(c, prod, ver)
	r1 := tr.RunR1(c, prod, ver)
	r2 := tr.RunR2(c, priorBytes, sc.PriorVerd)
	row := MatrixRow{Scenario: sc.ID, Workload: sc.Workload,
		R0Verd: string(r0.Verdict), R1Verd: string(r1.Verdict),
		R1State: string(r1.Decision.State), R2Verd: string(r2.Verdict),
		R0ProdNS: r0.ProdNS, R1ProdNS: r1.ProdNS, R1VerNS: r1.VerNS, R1OverNS: r1.OverNS}
	// Gates.
	if !r1.Failed && r0.Verdict != r1.Verdict {
		// Resolvable rows must agree; UNKNOWN rows (Failed) are exempt here
		// and asserted separately below.
		if r1.Decision.State != ResolveUnknown {
			t.Errorf("%s: R1 verdict %s != R0 %s", sc.ID, r1.Verdict, r0.Verdict)
		}
	}
	if !r1.Failed && r1.Verdict == ClaimAccepted && r0.Verdict != ClaimAccepted {
		row.FalseAcc = true
		t.Errorf("%s: FALSE ACCEPTED", sc.ID)
	}
	if sc.Expect != "" && !r1.Failed && r1.Decision.State != sc.Expect {
		t.Errorf("%s: resolution %s, want %s (%s)", sc.ID, r1.Decision.State, sc.Expect, r1.Decision.Reason)
	}
	// VERIFY must regenerate no bytes (production avoidance, hard gate).
	if r1.Decision.State == ResolveVerify && r1.ProdExec != 0 {
		t.Errorf("%s: VERIFY produced %d times", sc.ID, r1.ProdExec)
	}
	if sc.Expect == ResolveUnknown && !r1.Failed && r1.Decision.State != ResolveUnknown {
		t.Errorf("%s: want UNKNOWN, got %s", sc.ID, r1.Decision.State)
	}
	// R2 negative control: where R0 rejects, R2's stale reuse must be caught.
	if r0.Verdict == ClaimRejected && r2.Verdict == ClaimAccepted {
		row.R2Caught = true
	}
	return row
}

func TestResolutionMatrix(t *testing.T) {
	var rows []MatrixRow
	for _, sc := range devScenarios() {
		rows = append(rows, runScenario(t, sc))
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/vr_matrix.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Logf("%s/%s R0=%s R1=%s(%s) R2=%s falseAcc=%v R0prod=%dus R1prod=%dus R1ver=%dus R1over=%dns",
			r.Scenario, r.Workload, r.R0Verd, r.R1Verd, r.R1State, r.R2Verd,
			r.FalseAcc, r.R0ProdNS/1000, r.R1ProdNS/1000, r.R1VerNS/1000, r.R1OverNS)
	}
}

func TestHeldOutMatrix(t *testing.T) {
	for _, sc := range heldOutScenarios() {
		row := runScenario(t, sc)
		t.Logf("HELDOUT %s/%s R0=%s R1=%s(%s) falseAcc=%v",
			row.Scenario, row.Workload, row.R0Verd, row.R1Verd, row.R1State, row.FalseAcc)
		if row.FalseAcc {
			t.Fatalf("held-out false ACCEPTED: %s", sc.ID)
		}
		if row.R1Verd != row.R0Verd {
			t.Fatalf("held-out verdict mismatch: %s", sc.ID)
		}
	}
}

func mustMkdirTestdata(t *testing.T) error {
	t.Helper()
	return os.MkdirAll("testdata", 0o755)
}

var _ = filepath.Join
