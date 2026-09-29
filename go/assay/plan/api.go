package plan

// api.go: minimal exported construction API for downstream assay packages
// (replan). Additive only; no changes to existing semantics.

// NewRegistry returns an empty op registry.
func NewRegistry() *Registry { return standardRegistry() }

// RegisterOp binds the standard deterministic op + verifier under op's contract.
func RegisterOp(r *Registry, op string) { regOp(r, op) }

// ContractFor returns the canonical verifier-contract digest for an op label.
func ContractFor(op string) string { return contractFor(op) }

// StdOp is the standard deterministic assay operation (real synthetic CPU
// work scaled by CostUnits, content-bound output bytes).
func StdOp(node PlanNode, inputs map[string][]byte) ([]byte, error) {
	return digestOp(node, inputs)
}

// StdVerify is the independent recompute verifier for StdOp outputs.
func StdVerify(node PlanNode, inputs map[string][]byte, output []byte) bool {
	return digestVerify(node, inputs, output)
}

// BuildD1 constructs the D1 staged/direct alternative pair for a doc digest.
func BuildD1(seed uint64, docDigest string) (staged, direct PhysicalPlan) {
	return buildD1(seed, docDigest)
}

// BuildD2 constructs the D2 staged/direct pair.
func BuildD2(seed uint64, treeDigest, toolchain, lintCfg string) (staged, direct PhysicalPlan) {
	return buildD2(seed, treeDigest, toolchain, lintCfg)
}

// D1Doc derives the deterministic synthetic doc digest for seed.
func D1Doc(seed uint64) string { return d1Doc(seed) }

// D2Tree derives the deterministic synthetic tree digest for seed.
func D2Tree(seed uint64) string { return d2Tree(seed) }
