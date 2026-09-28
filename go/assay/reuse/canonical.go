package reuse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Fail-closed sentinel errors.
var (
	// ErrMissingDep: a required dependency (or key field) has no digest.
	ErrMissingDep = errors.New("reuse: missing required dependency metadata")
	// ErrKeyConflict: two different ExecutionKeys alias to one record.
	ErrKeyConflict = errors.New("reuse: execution-key conflict")
	// ErrNotAccepted: publish attempted for a non-ACCEPTED verification.
	ErrNotAccepted = errors.New("reuse: only ACCEPTED executions are publishable")
)

// canonical.go: deterministic canonical serialization and content digests.

// DigestBytes returns the lowercase hex SHA-256 of data.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestString digests one string value.
func DigestString(s string) string { return DigestBytes([]byte(s)) }

func looksLikeDigest(s string) bool {
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

// writeField appends one length-prefixed field: "name:byteLen:bytes|".
// Length-prefixing makes the encoding injective (no boundary ambiguity).
func writeField(sb *strings.Builder, name string, value string) {
	fmt.Fprintf(sb, "%s:%d:%s|", name, len(value), value)
}

// Canonical serializes the key deterministically: fixed field order, deps
// sorted by name, length-prefixed framing.
func (k ExecutionKey) Canonical() []byte {
	var sb strings.Builder
	writeField(&sb, "op", k.Operation)
	writeField(&sb, "opver", k.OperationVersion)
	writeField(&sb, "input", k.InputDigest)
	for _, d := range k.SortedDeps() {
		writeField(&sb, "dep:"+d.Name, d.Digest)
	}
	writeField(&sb, "exec", k.Executor)
	writeField(&sb, "env", k.EnvDigest)
	writeField(&sb, "verifier", k.VerifierContract)
	writeField(&sb, "policy", k.PolicyDigest)
	return []byte(sb.String())
}

// KeyDigest is SHA-256 over the canonical serialization.
func (k ExecutionKey) KeyDigest() string { return DigestBytes(k.Canonical()) }

// CurrentDeps materializes the full dependency set the key asserts, including
// the primary input, executor, env, verifier contract and policy as named deps
// so evaluation compares one uniform list.
func (k ExecutionKey) CurrentDeps() []Dep {
	deps := []Dep{
		{Name: "input", Digest: k.InputDigest},
		{Name: "executor", Digest: DigestString(k.Executor)},
		{Name: "env", Digest: envOrEmpty(k.EnvDigest)},
		{Name: "verifier_contract", Digest: k.VerifierContract},
		{Name: "policy", Digest: policyOrEmpty(k.PolicyDigest)},
	}
	deps = append(deps, k.Deps...)
	return deps
}

func envOrEmpty(d string) string {
	if d == "" {
		return DigestString("")
	}
	return d
}

func policyOrEmpty(d string) string {
	if d == "" {
		return DigestString("")
	}
	return d
}
