package verifyresolution

import (
	"fmt"
)

// resolve.go: the deterministic REUSE/VERIFY/RECOMPUTE/UNKNOWN resolver.

// Resolution is the resolver output state.
type Resolution string

const (
	ResolveReuse     Resolution = "REUSE"
	ResolveVerify    Resolution = "VERIFY"
	ResolveRecompute Resolution = "RECOMPUTE"
	ResolveUnknown   Resolution = "UNKNOWN"
)

// Decision is one auditable, machine-readable resolution.
type Decision struct {
	State     Resolution `json:"state"`
	Reason    string     `json:"reason"`
	CompKey   string     `json:"comp_digest"`
	VerifyKey string     `json:"verify_digest"`
	Artifact  string     `json:"artifact_digest"`
}

// Resolver answers REUSE/VERIFY/RECOMPUTE/UNKNOWN over a Store.
type Resolver struct {
	Store *Store
}

// Resolve determines what the required verification needs.
// liveComp maps dep name -> current digest for the ComputationKey's deps;
// liveVerify maps dep name -> current digest for the VerificationKey's deps;
// authority resolves current evidence status.
func (r *Resolver) Resolve(ck ComputationKey, vk VerificationKey,
	liveComp, liveVerify map[string]string, authority AuthorityLookup) Decision {
	kd, vd := ck.Digest(), vk.Digest()
	fail := func(st Resolution, reason string) Decision {
		return Decision{State: st, Reason: reason, CompKey: kd, VerifyKey: vd}
	}
	if err := ck.Validate(); err != nil {
		return fail(ResolveUnknown, "computation identity unestablished: "+err.Error())
	}
	if err := vk.Validate(); err != nil {
		return fail(ResolveUnknown, "verification identity unestablished: "+err.Error())
	}
	art, ok := r.Store.LookupArtifact(kd)
	if !ok {
		return fail(ResolveRecompute, "no artifact for computation key")
	}
	dec := Decision{CompKey: kd, VerifyKey: vd, Artifact: art.ArtifactDigest}
	// 1. Computation-dependency check: any change forces RECOMPUTE (never
	// downgraded to VERIFY).
	want := map[string]string{}
	for _, d := range art.CompDeps {
		want[d.Name] = d.Digest
	}
	for name, digest := range want {
		cur, ok := liveComp[name]
		if !ok || cur == "" || !validDigest(cur) {
			return fail(ResolveUnknown, "computation dep "+name+" unresolvable")
		}
		if cur != digest {
			dec.State, dec.Reason = ResolveRecompute, "computation dep "+name+" changed"
			return dec
		}
	}
	// 2. Integrity: bytes must be present and match the bound digest.
	// Corrupt/missing bytes yield RECOMPUTE when deps resolve, else UNKNOWN —
	// never VERIFY against untrusted bytes.
	body, ok := r.Store.Body(art.ArtifactDigest)
	if !ok || DigestBytes(body) != art.ArtifactDigest {
		if len(want) > 0 {
			dec.State, dec.Reason = ResolveRecompute, "artifact bytes untrusted"
			return dec
		}
		return fail(ResolveUnknown, "artifact bytes untrusted and deps unknown")
	}
	// 3. Verification-key binding: the required key must name THESE bytes.
	if vk.ArtifactDigest != art.ArtifactDigest {
		return fail(ResolveUnknown, "verification key names different bytes")
	}
	// 4. Live verification deps must resolve (fail-closed on missing
	// knowledge). Change detection lives in key rotation itself: different
	// dep values produce a different VerificationKey digest, which misses
	// below. Comparing the required key against itself would be vacuous, so
	// this step only establishes resolvability.
	for _, d := range vk.VerifyDeps {
		cur, ok := liveVerify[d.Name]
		if !ok || cur == "" || !validDigest(cur) {
			return fail(ResolveUnknown, "verification dep "+d.Name+" unresolvable")
		}
	}
	// 5. Claim lookup under the EXACT required key (never across keys).
	claim, ok := r.Store.LookupClaim(vd)
	if !ok {
		dec.State, dec.Reason = ResolveVerify, r.missReason(art.ArtifactDigest, vd)
		return dec
	}
	if claim.ArtifactDigest != art.ArtifactDigest {
		dec.State, dec.Reason = ResolveVerify, "claim binds different bytes"
		return dec
	}
	if claim.Status != ClaimAccepted || claim.Revoked {
		dec.State, dec.Reason = ResolveVerify, fmt.Sprintf("claim %s revoked=%v", claim.Status, claim.Revoked)
		return dec
	}
	// 6. Authority currency: the bound evidence must still be authoritative.
	if authority == nil {
		return fail(ResolveUnknown, "no authority view")
	}
	auth := authority(claim.EvidenceID)
	if !auth.Found {
		return fail(ResolveUnknown, "evidence authority unknown")
	}
	if auth.Version != claim.AuthVersion || auth.Status != "ACCEPTED" {
		dec.State, dec.Reason = ResolveVerify,
			fmt.Sprintf("evidence superseded: v%d/%s -> v%d/%s",
				claim.AuthVersion, claim.Status, auth.Version, auth.Status)
		return dec
	}
	dec.State, dec.Reason = ResolveReuse, "exact artifact + current ACCEPTED claim"
	return dec
}

// missReason explains a claim miss by citing prior claims over the same
// bytes (audit quality; the decision stays VERIFY either way).
func (r *Resolver) missReason(artifactDigest, required string) string {
	priors := r.Store.ClaimKeysForArtifact(artifactDigest)
	if len(priors) == 0 {
		return "no claim for required verification key"
	}
	short := func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	}
	_ = required
	return "no current claim; " + fmt.Sprint(len(priors)) + " prior claim(s), e.g. " + short(priors[0])
}
