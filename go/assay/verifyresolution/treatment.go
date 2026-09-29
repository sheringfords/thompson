package verifyresolution

import (
	"fmt"
	"time"
)

// treatment.go: R0/R1/R2 runners shared by the matrix, revocation,
// economics and boundary tests. R0 always reproduces; R1 resolves;
// R2 reuses stale claims (negative control — never viable).

// Case is one frozen evaluation case.
type Case struct {
	Workload string // "W1" | "W2"
	Seed     uint64
	Docs     []DocSource
	Params   map[string]string
	Burn     int // W2 production cost factor
	Prior    ContractSpec
	Required ContractSpec
	// Mutate, when non-nil, alters the computation inputs between history
	// setup (under Prior) and resolution (under Required).
	Mutate func(*Case)
	// Corrupt tampers stored bytes before resolution (adversarial).
	Corrupt bool
	// DropDep removes a computation dep from the live world (UNKNOWN probe).
	DropDep string
	// VerifyDeps are verification-side deps bound into VerificationKeys.
	VerifyDeps []Dep
	// DropVerifyDep removes a verification dep from the live world.
	DropVerifyDep string
}

// ProdFunc produces artifact bytes; VerifyFunc checks them under a contract.
type ProdFunc func(c Case) []byte
type VerifyFunc func(body []byte, c Case, contract ContractSpec) ClaimStatus

func w1Prod(c Case) []byte { return W1Produce(c.Docs, c.Params) }
func w1Ver(body []byte, c Case, k ContractSpec) ClaimStatus {
	return W1Verify(body, c.Docs, k)
}
func w2Prod(c Case) []byte {
	return W2Produce([]byte(fmt.Sprintf("seed-%d", c.Seed)), c.Params, c.Burn)
}
func w2Ver(body []byte, c Case, k ContractSpec) ClaimStatus {
	return W2Verify(body, c.Params, k)
}

// CompKey builds the ComputationKey for a case (NO verifier fields).
func CompKey(c Case) ComputationKey {
	op := "w1.produce"
	if c.Workload == "W2" {
		op = "w2.produce"
	}
	inputs, deps := caseInputs(c)
	return ComputationKey{Operation: op, OpVersion: "v1", Inputs: inputs,
		CompDeps: deps, Executor: "assay/1.0", Env: DigestString(""),
		Generation: DigestString(fmt.Sprintf("%v", c.Params) + fmt.Sprint(c.Burn))}
}

func caseInputs(c Case) ([]Dep, []Dep) {
	if c.Workload == "W1" {
		var in []Dep
		for _, d := range c.Docs {
			in = append(in, Dep{Name: "doc:" + d.ID, Digest: DigestString(d.ID + d.Title + d.Body + fmt.Sprint(d.Amount))})
		}
		return in, nil
	}
	return []Dep{{Name: "seed", Digest: DigestString(fmt.Sprintf("seed-%d", c.Seed))}}, nil
}

// VerifyKey builds the VerificationKey for artifact bytes + contract.
func VerifyKey(artifact []byte, c ContractSpec, verifierVer string, extra []Dep) VerificationKey {
	return VerificationKey{ArtifactDigest: DigestBytes(artifact),
		Contract: ContractDigest(c), VerifierID: "assay-verifier",
		VerifierVer: verifierVer, VerifyDeps: extra}
}

// LiveVerify materializes the current verification dep world.
func LiveVerify(c Case) map[string]string {
	m := map[string]string{}
	for _, d := range c.VerifyDeps {
		if c.DropVerifyDep != "" && d.Name == c.DropVerifyDep {
			continue
		}
		m[d.Name] = d.Digest
	}
	return m
}

// LiveComp materializes the current computation dep world for a case.
func LiveComp(c Case) map[string]string {
	m := map[string]string{}
	in, deps := caseInputs(c)
	for _, d := range append(in, deps...) {
		if c.DropDep != "" && d.Name == c.DropDep {
			continue
		}
		m[d.Name] = d.Digest
	}
	return m
}

// Outcome authority table (assay stand-in for journal replay).
type outcomeTable struct {
	m map[string]Authority
}

func newOutcomeTable() *outcomeTable { return &outcomeTable{m: map[string]Authority{}} }

func (o *outcomeTable) lookup(id string) Authority {
	if a, ok := o.m[id]; ok {
		return a
	}
	return Authority{}
}

func (o *outcomeTable) accept(id string, v uint64) {
	o.m[id] = Authority{Version: v, Status: "ACCEPTED", Found: true}
}

func (o *outcomeTable) revoke(id string) {
	cur := o.m[id]
	cur.Version++
	cur.Status = "REJECTED"
	cur.Found = true
	o.m[id] = cur
}

// Outcome is one R0/R1/R2 run result.
type Outcome struct {
	Treatment string
	Bytes     []byte
	Verdict   ClaimStatus
	Decision  Decision
	ProdExec  int
	VerExec   int
	ProdNS    int64
	VerNS     int64
	OverNS    int64
	Failed    bool // no bytes/verdict (UNKNOWN or refusal)
}

// TRunner executes treatments against a store.
type TRunner struct {
	Store *Store
	Auth  *outcomeTable
	Seq   int
}

func (t *TRunner) evidence(tag string) string {
	t.Seq++
	return fmt.Sprintf("ev-%s-%d", tag, t.Seq)
}

// SetupHistory publishes the artifact and records the PRIOR claim (accepted
// or rejected per priorVerdict), returning the bytes.
func (t *TRunner) SetupHistory(c Case, prod ProdFunc, ver VerifyFunc, priorVerdict ClaimStatus) []byte {
	ck := CompKey(c)
	t0 := time.Now()
	body := prod(c)
	prodNS := time.Since(t0).Nanoseconds()
	_ = prodNS
	rec, err := t.Store.PublishArtifact(ck, body, "rcpt-setup", 0)
	if err != nil {
		panic(err)
	}
	_ = rec
	ev := t.evidence("setup")
	t.Auth.accept(ev, 1)
	vk := VerifyKey(body, c.Prior, "v1", c.VerifyDeps)
	if _, err := t.Store.RecordClaim(vk, priorVerdict, ev, 1); err != nil {
		panic(err)
	}
	return body
}

// RunR0 reproduces fresh (oracle): produce + verify under Required.
func (t *TRunner) RunR0(c Case, prod ProdFunc, ver VerifyFunc) Outcome {
	var o Outcome
	o.Treatment = "R0"
	t0 := time.Now()
	o.Bytes = prod(c)
	o.ProdNS = time.Since(t0).Nanoseconds()
	o.ProdExec = 1
	t1 := time.Now()
	o.Verdict = ver(o.Bytes, c, c.Required)
	o.VerNS = time.Since(t1).Nanoseconds()
	o.VerExec = 1
	return o
}

// RunR1 resolves then acts: REUSE (no work), VERIFY (integrity + verify, no
// production), RECOMPUTE (produce + verify), UNKNOWN (fail, no bytes).
func (t *TRunner) RunR1(c Case, prod ProdFunc, ver VerifyFunc) Outcome {
	var o Outcome
	o.Treatment = "R1"
	ck := CompKey(c)
	t0 := time.Now()
	// Required verification key describes the CURRENT contract over the
	// bytes the computation WOULD produce — but bytes are unknown before
	// resolution. Resolve in two steps: first locate the artifact by
	// ComputationKey, then bind the required verification key to it.
	art, ok := t.Store.LookupArtifact(ck.Digest())
	var vk VerificationKey
	if ok {
		if b, okb := t.Store.Body(art.ArtifactDigest); okb {
			vk = VerifyKey(b, c.Required, "v1", c.VerifyDeps)
		}
	}
	var dec Decision
	if !ok {
		dec = Decision{State: ResolveRecompute, Reason: "no artifact for computation key",
			CompKey: ck.Digest()}
	} else {
		dec = (&Resolver{Store: t.Store}).Resolve(ck, vk, LiveComp(c), LiveVerify(c), t.Auth.lookup)
	}
	o.OverNS = time.Since(t0).Nanoseconds()
	o.Decision = dec
	switch dec.State {
	case ResolveReuse:
		b, _ := t.Store.Body(dec.Artifact)
		o.Bytes = b
		cl, _ := t.Store.LookupClaim(dec.VerifyKey)
		o.Verdict = cl.Status
	case ResolveVerify:
		b, _ := t.Store.Body(dec.Artifact)
		t1 := time.Now()
		intact := DigestBytes(b) == dec.Artifact
		o.OverNS += time.Since(t1).Nanoseconds()
		if !intact {
			o.Failed = true
			return o
		}
		t2 := time.Now()
		v := ver(b, c, c.Required)
		o.VerNS = time.Since(t2).Nanoseconds()
		o.VerExec = 1
		o.Bytes = b
		o.Verdict = v
		ev := t.evidence("verify")
		t.Auth.accept(ev, 1)
		nvk := VerifyKey(b, c.Required, "v1", c.VerifyDeps)
		if _, err := t.Store.RecordClaim(nvk, v, ev, 1); err != nil {
			panic(err)
		}
	case ResolveRecompute:
		t3 := time.Now()
		o.Bytes = prod(c)
		o.ProdNS = time.Since(t3).Nanoseconds()
		o.ProdExec = 1
		t4 := time.Now()
		o.Verdict = ver(o.Bytes, c, c.Required)
		o.VerNS = time.Since(t4).Nanoseconds()
		o.VerExec = 1
		rec, err := t.Store.PublishArtifact(ck, o.Bytes, "rcpt-r1", 0)
		if err != nil {
			panic(err)
		}
		_ = rec
		ev := t.evidence("recompute")
		t.Auth.accept(ev, 1)
		nvk := VerifyKey(o.Bytes, c.Required, "v1", c.VerifyDeps)
		if _, err := t.Store.RecordClaim(nvk, o.Verdict, ev, 1); err != nil {
			panic(err)
		}
	default:
		o.Failed = true
	}
	return o
}

// RunR2 reuses the PRIOR claim under the required contract without
// verification (negative control): returns prior bytes + prior verdict.
func (t *TRunner) RunR2(c Case, priorBytes []byte, priorVerdict ClaimStatus) Outcome {
	return Outcome{Treatment: "R2", Bytes: priorBytes, Verdict: priorVerdict}
}
