// Package artifactresolver answers one question about verified computation:
// do I need to recompute this artifact, or do I only need to prove the
// existing artifact again?
//
// It separates two facts that conventional caches conflate: the identity of
// the computation that produced bytes (ComputationKey) and the identity of a
// verification claim over those exact bytes (VerificationKey). Resolution is
// a deterministic four-way answer: REUSE, VERIFY, RECOMPUTE or UNKNOWN,
// always with a machine-readable reason, always fail-closed.
//
// The package is standard-library-only and depends on no execution,
// planning, routing or workflow machinery.
package artifactresolver

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
	ErrMissingMeta = errors.New("artifactresolver: missing required identity/evidence metadata")
	ErrConflict    = errors.New("artifactresolver: same computation key binds different bytes")
)

// Dep is one named content digest.
type Dep struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// ComputationKey is the exact identity of production semantics: operation,
// operation version, canonical primary inputs, computation-affecting
// dependency digests, and executor/environment/generation identity where
// output semantics depend on them. It answers what bytes should exist. It
// never carries verifier identity unless the verifier also generates bytes.
type ComputationKey struct {
	Operation  string `json:"operation"`
	OpVersion  string `json:"op_version"`
	Inputs     []Dep  `json:"inputs"`
	CompDeps   []Dep  `json:"comp_deps"`
	Executor   string `json:"executor"`
	Env        string `json:"env"`
	Generation string `json:"generation"`
}

// VerificationKey is the exact identity of a verification claim over
// specific artifact bytes: artifact digest, contract digest, verifier
// identity/version, and verification-affecting dependency digests.
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

// Canonical serializes the computation key deterministically (fixed field
// order, name-sorted dependencies, length-prefixed framing).
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

// CanonicalDeps returns the full computation-side dep set (inputs plus
// computation-affecting dependencies).
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
			return fmt.Errorf("artifactresolver: duplicate dep %q", d.Name)
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
			return fmt.Errorf("artifactresolver: duplicate verify dep %q", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

// ClaimStatus is the verification verdict bound to a claim.
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

// VerificationClaim binds a verdict to a VerificationKey. History is
// append-only: revocation marks, never rewrites, and never touches bytes.
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

// Authority is the current authoritative status of evidence, as resolved by
// the caller (journal replay, attestation service, or test fixture).
type Authority struct {
	Version uint64
	Status  string // ACCEPTED, REJECTED, UNKNOWN
	Found   bool
}

// AuthorityLookup resolves current authoritative evidence state.
type AuthorityLookup func(evidenceID string) Authority
