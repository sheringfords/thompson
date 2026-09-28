package reuse

import (
	"fmt"
	"sort"
)

// workload.go: two bounded research workloads (Phase 3) with deterministic
// synthetic data, frozen seeds, and one-dependency-at-a-time mutation sets.
// Synthetic/public data only. No production code, no customer data.

// Frozen seeds. DevSeeds were used during prototype development; HeldOutSeeds
// are reserved for the final zero-false-reuse gate (Phase 4 hard gate).
var DevSeeds = []uint64{11, 22, 33, 44, 55}

var HeldOutSeeds = []uint64{1001, 1002, 1003, 1004, 1005, 1006, 1007}

// Workload IDs.
const (
	WorkloadW1 = "W1"
	WorkloadW2 = "W2"
)

// splitmix64: deterministic PRNG (stable across Go versions; math/rand
// algorithms are version-stable too, but this keeps the assay self-contained).
type rng struct{ s uint64 }

func newRNG(seed uint64) *rng { return &rng{s: seed | 1} }

func (r *rng) next() uint64 {
	r.s += 0x9E3779B97F4A7C15
	z := r.s
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *rng) bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.next() >> 33)
	}
	return b
}

// SyntheticRepo is a deterministic synthetic source tree for W1.
type SyntheticRepo struct {
	Files map[string]string // path -> content
}

// GenRepo builds a repo with nFiles of ~fileBytes bytes from seed.
func GenRepo(seed uint64, nFiles, fileBytes int) SyntheticRepo {
	r := newRNG(seed)
	files := map[string]string{}
	for i := 0; i < nFiles; i++ {
		files[fmt.Sprintf("pkg/p%d/file%d.go", i%4, i)] =
			fmt.Sprintf("package p%d\n// seed %d\nvar x%d = %q\n", i%4, seed, i, r.bytes(fileBytes))
	}
	return SyntheticRepo{Files: files}
}

// TreeDigest is the canonical digest of the repo: sorted paths, length-framed.
func (repo SyntheticRepo) TreeDigest() string {
	paths := make([]string, 0, len(repo.Files))
	for p := range repo.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := ""
	for _, p := range paths {
		h += fmt.Sprintf("%d:%s|%d:%s|", len(p), p, len(repo.Files[p]), repo.Files[p])
	}
	return DigestString(h)
}

// SyntheticDoc is one deterministic synthetic document for W2.
type SyntheticDoc struct {
	ID     string
	Fields map[string]string // fixed schema fields
}

// GenDocs builds nDocs synthetic documents with schema fields from seed.
func GenDocs(seed uint64, nDocs int) []SyntheticDoc {
	r := newRNG(seed)
	fields := []string{"title", "date", "amount", "party", "clause"}
	out := make([]SyntheticDoc, nDocs)
	for i := range out {
		m := map[string]string{}
		for _, f := range fields {
			m[f] = fmt.Sprintf("%s-%d-%x", f, seed, r.bytes(8))
		}
		out[i] = SyntheticDoc{ID: fmt.Sprintf("doc-%d-%d", seed, i), Fields: m}
	}
	return out
}

// DocDigest digests one document canonically.
func (d SyntheticDoc) DocDigest() string {
	keys := make([]string, 0, len(d.Fields))
	for k := range d.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := d.ID + "|"
	for _, k := range keys {
		h += fmt.Sprintf("%d:%s|%d:%s|", len(k), k, len(d.Fields[k]), d.Fields[k])
	}
	return DigestString(h)
}

// W1Validate is the deterministic W1 computation: tree hash folded with
// config, lockfile, toolchain and command. Returns artifact bytes.
func W1Validate(tree, config, lockfile, toolchain, command string) []byte {
	return []byte(DigestString("w1|"+tree+"|"+config+"|"+lockfile+"|"+toolchain+"|"+command) + "\nPASS")
}

// W1Verify is the independent W1 verifier: recomputes and compares.
func W1Verify(body []byte, tree, config, lockfile, toolchain, command string) bool {
	want := W1Validate(tree, config, lockfile, toolchain, command)
	if len(body) != len(want) {
		return false
	}
	for i := range body {
		if body[i] != want[i] {
			return false
		}
	}
	return true
}

// W2Extract is the deterministic W2 computation: schema-bound projection of
// document fields folded with schema/executor versions.
func W2Extract(doc SyntheticDoc, schemaVer, executorVer, promptVer string) []byte {
	keys := make([]string, 0, len(doc.Fields))
	for k := range doc.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := "w2|" + schemaVer + "|" + executorVer + "|" + promptVer + "|" + doc.DocDigest() + "|"
	for _, k := range keys {
		h += fmt.Sprintf("%s=%s;", k, doc.Fields[k])
	}
	return []byte(DigestString(h))
}

// W2Verify is the independent field-level verifier: every schema field must
// be derivable from the source document (prefix check on the projection).
func W2Verify(body []byte, doc SyntheticDoc, schemaVer, executorVer, promptVer string) bool {
	want := W2Extract(doc, schemaVer, executorVer, promptVer)
	if len(body) != len(want) {
		return false
	}
	for i := range body {
		if body[i] != want[i] {
			return false
		}
	}
	return true
}

// World holds the full current dependency values for one workload instance.
// Mutation operators copy-and-change exactly one field (or an irrelevant one).
type World struct {
	Workload        string
	Seed            uint64
	Tree            SyntheticRepo // W1
	Doc             SyntheticDoc  // W2
	Config          string
	Lockfile        string
	Toolchain       string
	Command         string
	Env             string
	SchemaVer       string // W2
	ExecutorVer     string // W2
	PromptVer       string // W2
	Verifier        string // verifier contract version label
	Irrelevant      string // hostname/run-id/wall-clock: never in the key
	CorrectRevoked  bool   // authoritative correction revokes the outcome
	VerifierChanged bool   // verifier contract changed (must invalidate)
}

// BaseWorld builds the unmutated world for seed.
func BaseWorld(workload string, seed uint64) World {
	w := World{Workload: workload, Seed: seed, Irrelevant: "host-a/run-1"}
	if workload == WorkloadW1 {
		w.Tree = GenRepo(seed, 12, 64)
		w.Config = "cfg-v1"
		w.Lockfile = "lock-v1"
		w.Toolchain = "go1.22.0"
		w.Command = "go test ./..."
		w.Env = "GOOS=linux"
		w.Verifier = "w1-verifier/v2"
	} else {
		docs := GenDocs(seed, 4)
		w.Doc = docs[0]
		w.SchemaVer = "schema/v4"
		w.ExecutorVer = "extractor/1.3.0"
		w.PromptVer = "prompt/v7"
		w.Verifier = "w2-verifier/v1"
	}
	return w
}

// KeyFor builds the ExecutionKey the assay binds for world w.
func KeyFor(w World) ExecutionKey {
	if w.Workload == WorkloadW1 {
		return ExecutionKey{
			Operation:        "w1.validate",
			OperationVersion: "v3",
			InputDigest:      w.Tree.TreeDigest(),
			Deps: []Dep{
				{Name: "config", Digest: DigestString(w.Config)},
				{Name: "lockfile", Digest: DigestString(w.Lockfile)},
				{Name: "command", Digest: DigestString(w.Command)},
			},
			Executor:         "toolchain/" + w.Toolchain,
			EnvDigest:        DigestString(w.Env),
			VerifierContract: DigestString(w.Verifier),
			PolicyDigest:     DigestString("policy-default"),
		}
	}
	return ExecutionKey{
		Operation:        "w2.extract",
		OperationVersion: "v1",
		InputDigest:      w.Doc.DocDigest(),
		Deps: []Dep{
			{Name: "schema", Digest: DigestString(w.SchemaVer)},
			{Name: "prompt", Digest: DigestString(w.PromptVer)},
		},
		Executor:         "executor/" + w.ExecutorVer,
		EnvDigest:        "",
		VerifierContract: DigestString(w.Verifier),
		PolicyDigest:     DigestString("policy-default"),
	}
}

// Execute runs the workload computation fresh and returns artifact bytes plus
// modeled execution cost (USD). Cost is modeled, not slept: the assay measures
// real reuse-machinery overhead and compares against modeled execution cost,
// reporting the break-even point explicitly.
func Execute(w World) (body []byte, costUSD float64) {
	if w.Workload == WorkloadW1 {
		return W1Validate(w.Tree.TreeDigest(), w.Config, w.Lockfile, w.Toolchain, w.Command), 0.50
	}
	return W2Extract(w.Doc, w.SchemaVer, w.ExecutorVer, w.PromptVer), 0.20
}

// Verify checks artifact bytes against the independent verifier.
func Verify(w World, body []byte) bool {
	if w.Workload == WorkloadW1 {
		return W1Verify(body, w.Tree.TreeDigest(), w.Config, w.Lockfile, w.Toolchain, w.Command)
	}
	return W2Verify(body, w.Doc, w.SchemaVer, w.ExecutorVer, w.PromptVer)
}

// Mutation is one frozen world-mutation with the expected B2 validity.
type Mutation struct {
	Name     string
	Mutate   func(*World)
	ExpectB2 Validity // expected verified-reuse validity after mutation
}

// Mutations lists the frozen one-at-a-time mutation set (Phase 3).
func Mutations(workload string) []Mutation {
	irrelevant := Mutation{Name: "irrelevant-metadata", Mutate: func(w *World) {
		w.Irrelevant = "host-b/run-99"
	}, ExpectB2: ValidityValid}
	verifier := Mutation{Name: "verifier-contract-change", Mutate: func(w *World) {
		w.Verifier += "-vNEXT"
		w.VerifierChanged = true
	}, ExpectB2: ValidityStale}
	correction := Mutation{Name: "authoritative-correction", Mutate: func(w *World) {
		w.CorrectRevoked = true
	}, ExpectB2: ValidityInvalid}
	if workload == WorkloadW1 {
		return []Mutation{
			{Name: "repeat-identical", Mutate: func(*World) {}, ExpectB2: ValidityValid},
			{Name: "one-source-file", Mutate: func(w *World) {
				for p, c := range w.Tree.Files {
					w.Tree.Files[p] = c + "// fix\n"
					break
				}
			}, ExpectB2: ValidityStale},
			{Name: "config-change", Mutate: func(w *World) { w.Config = "cfg-v2" }, ExpectB2: ValidityStale},
			{Name: "lockfile-change", Mutate: func(w *World) { w.Lockfile = "lock-v2" }, ExpectB2: ValidityStale},
			{Name: "toolchain-change", Mutate: func(w *World) { w.Toolchain = "go1.23.0" }, ExpectB2: ValidityStale},
			{Name: "command-change", Mutate: func(w *World) { w.Command = "go test -race ./..." }, ExpectB2: ValidityStale},
			{Name: "env-change", Mutate: func(w *World) { w.Env = "GOOS=darwin" }, ExpectB2: ValidityStale},
			irrelevant, verifier, correction,
		}
	}
	return []Mutation{
		{Name: "repeat-identical", Mutate: func(*World) {}, ExpectB2: ValidityValid},
		{Name: "source-document-change", Mutate: func(w *World) {
			w.Doc.Fields["amount"] = w.Doc.Fields["amount"] + "-amended"
		}, ExpectB2: ValidityStale},
		{Name: "schema-change", Mutate: func(w *World) { w.SchemaVer = "schema/v5" }, ExpectB2: ValidityStale},
		{Name: "executor-change", Mutate: func(w *World) { w.ExecutorVer = "extractor/1.4.0" }, ExpectB2: ValidityStale},
		{Name: "prompt-change", Mutate: func(w *World) { w.PromptVer = "prompt/v8" }, ExpectB2: ValidityStale},
		irrelevant, verifier, correction,
	}
}

// LiveOf materializes the key's claimed dependency set as a live-world map
// (exact-replay probe input for Evaluate).
func LiveOf(k ExecutionKey) map[string]string {
	m := map[string]string{}
	for _, d := range k.CurrentDeps() {
		m[d.Name] = d.Digest
	}
	return m
}
