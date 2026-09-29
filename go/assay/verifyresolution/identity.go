// Package verifyresolution implements the verify-resolution assay prototype
// (THOMPSON_VERIFY_RESOLUTION_ASSAY_V1): candidate split-identity types
// (ComputationKey vs VerificationKey) beside the UNCHANGED VerifiedArtifact
// V1 contract, plus a deterministic REUSE/VERIFY/RECOMPUTE/UNKNOWN resolver.
// Research harness only: stdlib only (no thompson imports — the resolver must
// work independently), no servers, no workflows, no external effects.
package verifyresolution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Fail-closed errors.
var (
	ErrMissingMeta = errors.New("verifyresolution: missing required identity/evidence metadata")
	ErrConflict    = errors.New("verifyresolution: same computation key binds different bytes")
)

// Dep is one named content digest.
type Dep struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// ComputationKey answers what bytes should be produced. It NEVER carries
// verifier identity unless the verifier also generates bytes.
type ComputationKey struct {
	Operation  string `json:"operation"`
	OpVersion  string `json:"op_version"`
	Inputs     []Dep  `json:"inputs"`
	CompDeps   []Dep  `json:"comp_deps"`
	Executor   string `json:"executor"`
	Env        string `json:"env"`
	Generation string `json:"generation"`
}

// VerificationKey answers what claim is tested about exact bytes.
type VerificationKey struct {
	ArtifactDigest string `json:"artifact_digest"`
	Contract       string `json:"contract"`
	VerifierID     string `json:"verifier_id"`
	VerifierVer    string `json:"verifier_ver"`
	VerifyDeps     []Dep  `json:"verify_deps"`
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestBytes hashes raw bytes.
func DigestBytes(data []byte) string { return digestHex(data) }

// DigestString hashes a string.
func DigestString(s string) string { return digestHex([]byte(s)) }

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func writeField(sb *strings.Builder, name, value string) {
	fmt.Fprintf(sb, "%s:%d:%s|", name, len(value), value)
}

func sortedDeps(ds []Dep) []Dep {
	out := append([]Dep(nil), ds...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Canonical serializes deterministically (fixed order, sorted deps, framing).
func (k ComputationKey) Canonical() []byte {
	var sb strings.Builder
	writeField(&sb, "op", k.Operation)
	writeField(&sb, "opver", k.OpVersion)
	for _, d := range sortedDeps(k.Inputs) {
		writeField(&sb, "in:"+d.Name, d.Digest)
	}
	for _, d := range sortedDeps(k.CompDeps) {
		writeField(&sb, "dep:"+d.Name, d.Digest)
	}
	writeField(&sb, "exec", k.Executor)
	writeField(&sb, "env", k.Env)
	writeField(&sb, "gen", k.Generation)
	return []byte(sb.String())
}

// Digest is the computation identity.
func (k ComputationKey) Digest() string { return digestHex(k.Canonical()) }

// CanonicalDeps returns the full computation-side dep set (inputs + comp deps).
func (k ComputationKey) CanonicalDeps() []Dep {
	out := append([]Dep(nil), k.Inputs...)
	out = append(out, k.CompDeps...)
	return out
}

// Validate enforces fail-closed construction.
func (k ComputationKey) Validate() error {
	if k.Operation == "" || k.OpVersion == "" {
		return fmt.Errorf("%w: operation identity", ErrMissingMeta)
	}
	if len(k.Inputs) == 0 {
		return fmt.Errorf("%w: primary inputs", ErrMissingMeta)
	}
	seen := map[string]bool{}
	for _, d := range append(append([]Dep(nil), k.Inputs...), k.CompDeps...) {
		if d.Name == "" || !validDigest(d.Digest) {
			return fmt.Errorf("%w: dep %q", ErrMissingMeta, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("verifyresolution: duplicate dep %q", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

// Canonical serializes the verification key deterministically.
func (k VerificationKey) Canonical() []byte {
	var sb strings.Builder
	writeField(&sb, "artifact", k.ArtifactDigest)
	writeField(&sb, "contract", k.Contract)
	writeField(&sb, "verifier", k.VerifierID)
	writeField(&sb, "verifierver", k.VerifierVer)
	for _, d := range sortedDeps(k.VerifyDeps) {
		writeField(&sb, "vdep:"+d.Name, d.Digest)
	}
	return []byte(sb.String())
}

// Digest is the claim identity.
func (k VerificationKey) Digest() string { return digestHex(k.Canonical()) }

// Validate enforces fail-closed construction.
func (k VerificationKey) Validate() error {
	if !validDigest(k.ArtifactDigest) {
		return fmt.Errorf("%w: artifact digest", ErrMissingMeta)
	}
	if !validDigest(k.Contract) {
		return fmt.Errorf("%w: contract digest", ErrMissingMeta)
	}
	if k.VerifierID == "" || k.VerifierVer == "" {
		return fmt.Errorf("%w: verifier identity", ErrMissingMeta)
	}
	seen := map[string]bool{}
	for _, d := range k.VerifyDeps {
		if d.Name == "" || !validDigest(d.Digest) {
			return fmt.Errorf("%w: verify dep %q", ErrMissingMeta, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("verifyresolution: duplicate verify dep %q", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

// ClaimStatus is the verification verdict.
type ClaimStatus string

const (
	ClaimAccepted ClaimStatus = "ACCEPTED"
	ClaimRejected ClaimStatus = "REJECTED"
	ClaimUnknown  ClaimStatus = "UNKNOWN"
)

// ArtifactRecord binds immutable bytes to a ComputationKey.
type ArtifactRecord struct {
	CompDigest     string `json:"comp_digest"`
	CompCanonical  string `json:"comp_canonical"`
	ArtifactDigest string `json:"artifact_digest"`
	ReceiptID      string `json:"receipt_id"`
	CostUnits      int64  `json:"cost_units"`
	CompDeps       []Dep  `json:"comp_deps"`
	ProducedAt     string `json:"produced_at"`
}

// VerificationClaim binds a verdict to a VerificationKey (history preserved;
// revocation never mutates bytes).
type VerificationClaim struct {
	KeyDigest      string      `json:"key_digest"`
	ArtifactDigest string      `json:"artifact_digest"`
	Status         ClaimStatus `json:"status"`
	EvidenceID     string      `json:"evidence_id"`
	VerifiedAt     string      `json:"verified_at"`
	AuthVersion    uint64      `json:"auth_version"`
	Revoked        bool        `json:"revoked"`
	SupersededBy   string      `json:"superseded_by,omitempty"`
}

// Authority is the current authoritative status of evidence.
type Authority struct {
	Version uint64
	Status  string // ACCEPTED, REJECTED, UNKNOWN
	Found   bool
}

// AuthorityLookup resolves current authoritative evidence state.
type AuthorityLookup func(evidenceID string) Authority
