package artifactresolver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// parity_test.go: Phase 5 semantic parity against PR #33 (frozen fixture
// testdata/parity_frozen.json, generated once from the assay implementation
// via an external exporter — no research-branch dependency). Replays the
// recorded op sequences through this package and requires EXACT (state,
// reason, artifact) equality plus matching history shape (counts; timestamps
// intentionally excluded — wall-clock vs frozen assay clock, documented).
// Zero discrepancies permitted; any intentional difference would fail here.

type parityDep struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type parityCK struct {
	Op     string      `json:"op"`
	OpVer  string      `json:"opVer"`
	Exec   string      `json:"exec"`
	Env    string      `json:"env"`
	Gen    string      `json:"gen"`
	Inputs []parityDep `json:"inputs"`
	Deps   []parityDep `json:"deps"`
}

type parityVK struct {
	Artifact string      `json:"artifact"`
	Contract string      `json:"contract"`
	VID      string      `json:"vID"`
	VVer     string      `json:"vVer"`
	Deps     []parityDep `json:"deps"`
}

type parityOp struct {
	Type        string    `json:"type"`
	Comp        *parityCK `json:"comp"`
	Body        string    `json:"body"`
	Receipt     string    `json:"receipt"`
	VKey        *parityVK `json:"vkey"`
	Status      string    `json:"status"`
	Evidence    string    `json:"evidence"`
	AuthVersion uint64    `json:"auth_version"`
	RevokeKey   string    `json:"revoke_key"`
	SupBy       string    `json:"sup_by"`
}

type parityAuth struct {
	Version uint64 `json:"version"`
	Status  string `json:"status"`
	Found   bool   `json:"found"`
}

type parityResolveIn struct {
	Comp     parityCK              `json:"comp"`
	VKey     parityVK              `json:"vkey"`
	LiveComp map[string]string     `json:"live_comp"`
	LiveVer  map[string]string     `json:"live_ver"`
	Auth     map[string]parityAuth `json:"auth"`
}

type parityResolveOut struct {
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Artifact string `json:"artifact"`
}

type parityScenario struct {
	Name     string            `json:"name"`
	Ops      []parityOp        `json:"ops"`
	Resolves []parityResolveIn `json:"resolves"`
}

type parityExpected struct {
	Scenario string             `json:"scenario"`
	Resolves []parityResolveOut `json:"resolves"`
	Arts     int                `json:"artifacts"`
	Claims   int                `json:"claims"`
}

type parityFixture struct {
	Scenarios []parityScenario `json:"scenarios"`
	Expected  []parityExpected `json:"expected"`
}

func toDeps(ds []parityDep) []Dep {
	var out []Dep
	for _, d := range ds {
		out = append(out, Dep{Name: d.Name, Digest: d.Digest})
	}
	return out
}

func toCK(c parityCK) ComputationKey {
	return ComputationKey{Operation: c.Op, OpVersion: c.OpVer, Inputs: toDeps(c.Inputs),
		CompDeps: toDeps(c.Deps), Executor: c.Exec, Env: c.Env, Generation: c.Gen}
}

func toVK(v parityVK) VerificationKey {
	return VerificationKey{ArtifactDigest: v.Artifact, Contract: v.Contract,
		VerifierID: v.VID, VerifierVer: v.VVer, VerifyDeps: toDeps(v.Deps)}
}

func TestParityFrozen(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "parity_frozen.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx parityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Scenarios) != len(fx.Expected) {
		t.Fatal("fixture scenarios/expected mismatch")
	}
	discrepancies := 0
	for i, sc := range fx.Scenarios {
		exp := fx.Expected[i]
		if exp.Scenario != sc.Name {
			t.Fatalf("fixture order mismatch: %s vs %s", exp.Scenario, sc.Name)
		}
		s, err := Open(filepath.Join(t.TempDir(), "parity.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		auth := map[string]Authority{}
		for _, op := range sc.Ops {
			switch op.Type {
			case "artifact":
				if _, err := s.RecordArtifact(toCK(*op.Comp), []byte(op.Body), op.Receipt, 0); err != nil {
					t.Fatalf("%s: artifact op failed: %v", sc.Name, err)
				}
			case "claim":
				if _, err := s.RecordVerification(toVK(*op.VKey), ClaimStatus(op.Status), op.Evidence, op.AuthVersion); err != nil {
					t.Fatalf("%s: claim op failed: %v", sc.Name, err)
				}
				auth[op.Evidence] = Authority{Version: op.AuthVersion, Status: "ACCEPTED", Found: true}
			case "revoke":
				for _, prev := range sc.Ops {
					if prev.Type == "claim" {
						if _, err := s.RevokeVerification(toVK(*prev.VKey).Digest(), op.SupBy); err != nil {
							t.Fatalf("%s: revoke failed: %v", sc.Name, err)
						}
					}
				}
				for ev := range auth {
					a := auth[ev]
					a.Version++
					a.Status = "REJECTED"
					auth[ev] = a
				}
			case "corrupt":
				s.CorruptBody(op.RevokeKey, []byte(op.Body))
			default:
				t.Fatalf("%s: unknown op %q", sc.Name, op.Type)
			}
		}
		if len(sc.Resolves) != len(exp.Resolves) {
			t.Fatalf("%s: resolve count mismatch", sc.Name)
		}
		for j, ri := range sc.Resolves {
			want := exp.Resolves[j]
			dec := (&Resolver{Store: s}).Resolve(toCK(ri.Comp), toVK(ri.VKey), ri.LiveComp, ri.LiveVer,
				func(id string) Authority {
					if a, ok := ri.Auth[id]; ok {
						return Authority{Version: a.Version, Status: a.Status, Found: a.Found}
					}
					return Authority{}
				})
			if string(dec.State) != want.State || dec.Reason != want.Reason || dec.Artifact != want.Artifact {
				t.Errorf("%s[%d]: got (%s, %q, %.12s) want (%s, %q, %.12s)",
					sc.Name, j, dec.State, dec.Reason, dec.Artifact,
					want.State, want.Reason, want.Artifact)
				discrepancies++
			}
		}
		a, c, _ := s.Stats()
		if a != exp.Arts || c != exp.Claims {
			t.Errorf("%s: history shape (%d arts, %d claims) want (%d, %d)",
				sc.Name, a, c, exp.Arts, exp.Claims)
			discrepancies++
		}
		_ = s.Close()
	}
	if discrepancies != 0 {
		t.Fatalf("%d parity discrepancies (STOP: packaging changed semantics)", discrepancies)
	}
	t.Logf("parity: %d scenarios, zero discrepancies", len(fx.Scenarios))
}
