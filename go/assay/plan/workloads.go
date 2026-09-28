package plan

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// workloads.go: two bounded DAG workloads (Phase 3) over the generic executor.
// Synthetic data + synthetic CPU work ONLY, reported explicitly as such.
// Frozen seeds: dev 11..55, held-out 1001..1007 (shared with the reuse assay).

// WorkUnitHashes scales synthetic CPU work per cost unit: one unit = 2000
// SHA-256 compressions over a 1KB buffer (~tens of µs, measurable, deterministic).
const WorkUnitHashes = 2000

var workBuf = make([]byte, 1024)

func burnCPU(units int) {
	h := sha256.New()
	for i := 0; i < units*WorkUnitHashes; i++ {
		workBuf[i%1024] = byte(i >> 7)
		h.Write(workBuf)
		if i%512 == 511 {
			h.Sum(nil)
			h.Reset()
		}
	}
}

// digestOp is the generic deterministic op: output = digest(op, version,
// params, sorted upstream digests); work = CostUnits of synthetic CPU.
func digestOp(node PlanNode, inputs map[string][]byte) ([]byte, error) {
	burnCPU(node.CostUnits)
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := "op:" + node.Op + "@" + node.OpVersion + "|ex:" + node.Executor + "|"
	for _, d := range node.Inputs {
		h += fmt.Sprintf("in:%s=%s;", d.Name, d.Digest)
	}
	for _, k := range keys {
		h += fmt.Sprintf("up:%s=%x;", k, sha256.Sum256(inputs[k]))
	}
	return []byte(reuse.DigestString(h)), nil
}

// digestVerify is the independent op verifier: recompute without the CPU burn.
func digestVerify(node PlanNode, inputs map[string][]byte, output []byte) bool {
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := "op:" + node.Op + "@" + node.OpVersion + "|ex:" + node.Executor + "|"
	for _, d := range node.Inputs {
		h += fmt.Sprintf("in:%s=%s;", d.Name, d.Digest)
	}
	for _, k := range keys {
		h += fmt.Sprintf("up:%s=%x;", k, sha256.Sum256(inputs[k]))
	}
	want := reuse.DigestString(h)
	if len(output) != len(want) {
		return false
	}
	for i := range output {
		if output[i] != want[i] {
			return false
		}
	}
	return true
}

// Standard registry: every assay op uses digestOp; verifier labels are the
// contract digests bound at plan build (one label per op identity).
func standardRegistry() *Registry {
	return &Registry{Ops: map[string]OpFunc{}, Verifiers: map[string]VerifierFunc{}, Contracts: map[string]string{}}
}

func contractFor(op string) string { return reuse.DigestString("contract:" + op + "/v1") }

func regOp(r *Registry, op string) {
	r.Ops[op] = digestOp
	c := contractFor(op)
	r.Contracts[op] = c
	r.Verifiers[c] = digestVerify
}

// D1: structured-document pipeline.
// validate -> extract -> normalize -> {aggregate -> report -> attest (terminal),
// summary (second output sharing normalize)}.
// Alternatives: staged (below) vs direct (validate -> direct -> attest-direct).
func buildD1(seed uint64, docDigest string) (staged, direct PhysicalPlan) {
	vc := func(op string) string { return contractFor(op) }
	val := PlanNode{NodeID: "validate", Op: "d1.validate", OpVersion: "v1",
		Inputs:   []reuse.Dep{{Name: "doc", Digest: docDigest}},
		Executor: "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.validate"), PolicyDigest: reuse.DigestString("p"), CostUnits: 3}
	ext := PlanNode{NodeID: "extract", Op: "d1.extract", OpVersion: "v1",
		Upstreams: []string{"validate"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.extract"), PolicyDigest: reuse.DigestString("p"), CostUnits: 5}
	norm := PlanNode{NodeID: "normalize", Op: "d1.normalize", OpVersion: "v1",
		Upstreams: []string{"extract"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.normalize"), PolicyDigest: reuse.DigestString("p"), CostUnits: 4}
	agg := PlanNode{NodeID: "aggregate", Op: "d1.aggregate", OpVersion: "v1",
		Upstreams: []string{"normalize"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.aggregate"), PolicyDigest: reuse.DigestString("p"), CostUnits: 6}
	rep := PlanNode{NodeID: "report", Op: "d1.report", OpVersion: "v1",
		Upstreams: []string{"aggregate"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.report"), PolicyDigest: reuse.DigestString("p"), CostUnits: 2}
	att := PlanNode{NodeID: "attest", Op: "d1.attest", OpVersion: "v1",
		Upstreams: []string{"report"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.attest"), PolicyDigest: reuse.DigestString("p"), CostUnits: 1}
	sum := PlanNode{NodeID: "summary", Op: "d1.summary", OpVersion: "v1",
		Upstreams: []string{"normalize"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.summary"), PolicyDigest: reuse.DigestString("p"), CostUnits: 2}
	job := LogicalJob{JobID: fmt.Sprintf("d1-%d", seed),
		Inputs:           []reuse.Dep{{Name: "doc", Digest: docDigest}},
		RequiredFinal:    "attest",
		TerminalVerifier: vc("d1.attest")}
	staged = PhysicalPlan{PlanID: "d1-staged", Job: job,
		Nodes:    []PlanNode{val, ext, norm, agg, rep, att, sum},
		Terminal: "attest", Policy: reuse.DigestString("plan-default")}
	// Direct alternative: fewer stages, more expensive work.
	dir := PlanNode{NodeID: "direct", Op: "d1.direct", OpVersion: "v1",
		Upstreams: []string{"validate"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.direct"), PolicyDigest: reuse.DigestString("p"), CostUnits: 16}
	attD := PlanNode{NodeID: "attest", Op: "d1.attest", OpVersion: "v1",
		Upstreams: []string{"direct"},
		Executor:  "d1-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d1.attest"), PolicyDigest: reuse.DigestString("p"), CostUnits: 1}
	djob := job
	djob.RequiredFinal = "attest"
	direct = PhysicalPlan{PlanID: "d1-direct", Job: djob,
		Nodes:    []PlanNode{val, dir, attD},
		Terminal: "attest", Policy: reuse.DigestString("plan-default")}
	return staged, direct
}

// D2: coding-validation pipeline.
// identity -> compile -> {unit, lint} -> aggregate -> attest.
// unit/setup and lint/setup are DISTINCT node ids with IDENTICAL op+inputs
// (same ExecutionKey): the predeclared duplicate-key dedup case for P1 vs P2.
// Alternatives: staged vs direct (identity -> direct-check -> attest).
func buildD2(seed uint64, treeDigest, toolchain, lintCfg string) (staged, direct PhysicalPlan) {
	vc := func(op string) string { return contractFor(op) }
	ident := PlanNode{NodeID: "identity", Op: "d2.identity", OpVersion: "v1",
		Inputs:   []reuse.Dep{{Name: "tree", Digest: treeDigest}},
		Executor: "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.identity"), PolicyDigest: reuse.DigestString("p"), CostUnits: 2}
	comp := PlanNode{NodeID: "compile", Op: "d2.compile", OpVersion: "v1",
		Upstreams: []string{"identity"},
		Inputs:    []reuse.Dep{{Name: "toolchain", Digest: reuse.DigestString(toolchain)}},
		Executor:  "toolchain/" + toolchain, EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.compile"), PolicyDigest: reuse.DigestString("p"), CostUnits: 8}
	setupU := PlanNode{NodeID: "unit-setup", Op: "d2.setup", OpVersion: "v1",
		Upstreams: []string{"compile"},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.setup"), PolicyDigest: reuse.DigestString("p"), CostUnits: 3}
	setupL := PlanNode{NodeID: "lint-setup", Op: "d2.setup", OpVersion: "v1",
		Upstreams: []string{"compile"},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.setup"), PolicyDigest: reuse.DigestString("p"), CostUnits: 3}
	unit := PlanNode{NodeID: "unit", Op: "d2.unit", OpVersion: "v1",
		Upstreams: []string{"unit-setup"},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.unit"), PolicyDigest: reuse.DigestString("p"), CostUnits: 5}
	lint := PlanNode{NodeID: "lint", Op: "d2.lint", OpVersion: "v1",
		Upstreams: []string{"lint-setup"},
		Inputs:    []reuse.Dep{{Name: "lintcfg", Digest: reuse.DigestString(lintCfg)}},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.lint"), PolicyDigest: reuse.DigestString("p"), CostUnits: 5}
	agg := PlanNode{NodeID: "aggregate", Op: "d2.aggregate", OpVersion: "v1",
		Upstreams: []string{"unit", "lint"},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.aggregate"), PolicyDigest: reuse.DigestString("p"), CostUnits: 2}
	att := PlanNode{NodeID: "attest", Op: "d2.attest", OpVersion: "v1",
		Upstreams: []string{"aggregate"},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.attest"), PolicyDigest: reuse.DigestString("p"), CostUnits: 1}
	job := LogicalJob{JobID: fmt.Sprintf("d2-%d", seed),
		Inputs:           []reuse.Dep{{Name: "tree", Digest: treeDigest}},
		RequiredFinal:    "attest",
		TerminalVerifier: vc("d2.attest")}
	staged = PhysicalPlan{PlanID: "d2-staged", Job: job,
		Nodes:    []PlanNode{ident, comp, setupU, setupL, unit, lint, agg, att},
		Terminal: "attest", Policy: reuse.DigestString("plan-default")}
	chk := PlanNode{NodeID: "direct", Op: "d2.direct", OpVersion: "v1",
		Upstreams: []string{"identity"},
		Inputs:    []reuse.Dep{{Name: "toolchain", Digest: reuse.DigestString(toolchain)}},
		Executor:  "toolchain/" + toolchain, EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.direct"), PolicyDigest: reuse.DigestString("p"), CostUnits: 19}
	attD := PlanNode{NodeID: "attest", Op: "d2.attest", OpVersion: "v1",
		Upstreams: []string{"direct"},
		Executor:  "d2-tools/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: vc("d2.attest"), PolicyDigest: reuse.DigestString("p"), CostUnits: 1}
	direct = PhysicalPlan{PlanID: "d2-direct", Job: job,
		Nodes:    []PlanNode{ident, chk, attD},
		Terminal: "attest", Policy: reuse.DigestString("plan-default")}
	return staged, direct
}

// PlanSeeds reuses the frozen seed sets.
var PlanDevSeeds = []uint64{11, 22, 33}

var PlanHeldOutSeeds = []uint64{1001, 1002, 1003}

// WorldInputs derives deterministic synthetic inputs per seed.
func d1Doc(seed uint64) string {
	return reuse.DigestString(fmt.Sprintf("doc-%d", seed))
}

func d2Tree(seed uint64) string {
	return reuse.DigestString(fmt.Sprintf("tree-%d", seed))
}
