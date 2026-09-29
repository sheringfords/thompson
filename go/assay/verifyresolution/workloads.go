package verifyresolution

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// workloads.go: two bounded workloads (Phase 4) with frozen contract
// rotations. Real deterministic CPU (no sleeps); synthetic data only. The
// same generic resolver operates both. Frozen rotation fixtures + held-out
// set are declared here BEFORE evaluation (Phase 13 pre-registration).

// DocSource is one synthetic source document.
type DocSource struct {
	ID     string
	Title  string
	Body   string
	Amount int64
	Tags   []string
}

// GenSources builds n deterministic documents from seed.
func GenSources(seed uint64, n int) []DocSource {
	r := newSplitRng(seed)
	out := make([]DocSource, n)
	for i := range out {
		out[i] = DocSource{
			ID:     fmt.Sprintf("doc-%d-%04d", seed, i),
			Title:  fmt.Sprintf("title-%x", r.bytes(8)),
			Body:   fmt.Sprintf("body-%x-%x", r.bytes(32), r.bytes(32)),
			Amount: int64(r.next() % 50000),
			Tags:   []string{fmt.Sprintf("t%d", r.next()%5), fmt.Sprintf("u%d", r.next()%3)},
		}
	}
	return out
}

// W1Produce parses/extracts/normalizes a structured artifact (moderate cost).
func W1Produce(docs []DocSource, params map[string]string) []byte {
	ids := make([]string, 0, len(docs))
	byID := map[string]DocSource{}
	for _, d := range docs {
		ids = append(ids, d.ID)
		byID[d.ID] = d
	}
	sort.Strings(ids)
	h := sha256.New()
	h.Write([]byte("w1|" + params["extract"] + "|"))
	for _, id := range ids {
		d := byID[id]
		rec := fmt.Sprintf("%s|%s|%d|%s|", d.ID, strings.ToLower(d.Title), d.Amount, strings.Join(d.Tags, ","))
		sum := sha256.Sum256([]byte(rec))
		// Real per-record work: repeated hashing over record bytes.
		work := sum[:]
		for i := 0; i < 24; i++ {
			s := sha256.Sum256(append(work, byte(i)))
			work = s[:]
		}
		h.Write(work)
	}
	return []byte("w1artifact:" + fmt.Sprintf("%x", h.Sum(nil)))
}

// ContractSpec is one frozen verification contract revision.
type ContractSpec struct {
	ID      string
	Version string
	Rules   map[string]string // rule name -> parameter digest/label
}

// W1Contracts lists the frozen W1 rotation fixtures.
func W1Contracts() []ContractSpec {
	mk := func(id, ver string, rules map[string]string) ContractSpec {
		return ContractSpec{ID: id, Version: ver, Rules: rules}
	}
	return []ContractSpec{
		mk("w1-schema", "v1", map[string]string{"format": "strict", "amount-cap": "50000"}),
		mk("w1-schema", "v2-strict", map[string]string{"format": "strict", "amount-cap": "10000"}),
		mk("w1-schema", "v3-relaxed", map[string]string{"format": "loose", "amount-cap": "50000"}),
		mk("w1-schema", "v4-newfield", map[string]string{"format": "strict", "amount-cap": "50000", "tags-required": "yes"}),
		mk("w1-schema", "v5-noinvariant", map[string]string{"format": "strict"}),
		mk("w1-schema", "v6-bugfix", map[string]string{"format": "strict", "amount-cap": "50000", "title-nonempty": "yes"}),
	}
}

// W1Verify validates the artifact under a contract (independent re-derivation
// of structural checks; deterministic; real CPU over artifact bytes).
func W1Verify(artifact []byte, docs []DocSource, c ContractSpec) ClaimStatus {
	if !strings.HasPrefix(string(artifact), "w1artifact:") {
		return ClaimRejected
	}
	// Independent checks per contract rules (recomputed, not trusted).
	if cap, ok := c.Rules["amount-cap"]; ok {
		limit := int64(50000)
		if cap == "10000" {
			limit = 10000
		}
		for _, d := range docs {
			if d.Amount > limit {
				return ClaimRejected
			}
		}
	}
	if c.Rules["tags-required"] == "yes" {
		for _, d := range docs {
			if len(d.Tags) == 0 {
				return ClaimRejected
			}
		}
	}
	if c.Rules["title-nonempty"] == "yes" {
		for _, d := range docs {
			if strings.TrimSpace(d.Title) == "" {
				return ClaimRejected
			}
		}
	}
	// Proof-of-work over bytes (verification has real, measured cost).
	sum := sha256.Sum256(artifact)
	for i := 0; i < 8; i++ {
		sum = sha256.Sum256(append(sum[:], byte(i)))
	}
	_ = sum
	return ClaimAccepted
}

// W2Produce runs a deterministic CPU-EXPENSIVE transformation (production
// dominates verification by design; burn factor scales the ratio).
func W2Produce(seedBytes []byte, params map[string]string, burnFactor int) []byte {
	work := sha256.Sum256(append([]byte("w2|"+params["transform"]+"|"), seedBytes...))
	out := work[:]
	rounds := 2000 * burnFactor
	for i := 0; i < rounds; i++ {
		s := sha256.Sum256(append(out, byte(i), byte(i>>8), byte(i>>16)))
		out = s[:]
		if i%512 == 511 {
			// Mix in progress to prevent any shortcutting narrative.
			h := sha256.New()
			h.Write(out)
			h.Write(seedBytes)
			out = h.Sum(nil)
		}
	}
	return []byte("w2artifact:" + fmt.Sprintf("%x|burn=%d", sha256.Sum256(out), burnFactor))
}

// W2Contracts lists the frozen W2 rotation fixtures.
func W2Contracts() []ContractSpec {
	mk := func(id, ver string, rules map[string]string) ContractSpec {
		return ContractSpec{ID: id, Version: ver, Rules: rules}
	}
	return []ContractSpec{
		mk("w2-policy", "v1", map[string]string{"threshold": "t0", "invariant": "prefix"}),
		mk("w2-policy", "v2-new-invariant", map[string]string{"threshold": "t0", "invariant": "prefix", "suffix-check": "yes"}),
		mk("w2-policy", "v3-threshold", map[string]string{"threshold": "t1", "invariant": "prefix"}),
		mk("w2-policy", "v4-additional", map[string]string{"threshold": "t0", "invariant": "prefix", "parity": "even"}),
		mk("w2-policy", "v5-rotation", map[string]string{"threshold": "t0", "invariant": "prefix", "rotated": "yes"}),
	}
}

// W2Verify applies cheap deterministic checks (thresholds/invariants over
// artifact bytes + params). Threshold t1 rejects artifacts whose embedded
// burn factor exceeds 10 (a verification-only threshold change).
func W2Verify(artifact []byte, params map[string]string, c ContractSpec) ClaimStatus {
	s := string(artifact)
	if !strings.HasPrefix(s, "w2artifact:") {
		return ClaimRejected
	}
	if c.Rules["threshold"] == "t1" {
		var burn int
		if _, err := fmt.Sscanf(s, "w2artifact:%*x|burn=%d", &burn); err != nil {
			// Fallback parse for the hex|burn shape.
			parts := strings.Split(s, "|burn=")
			if len(parts) != 2 {
				return ClaimRejected
			}
			fmt.Sscanf(parts[1], "%d", &burn)
		}
		if burn > 10 {
			return ClaimRejected
		}
	}
	if c.Rules["parity"] == "even" {
		sum := sha256.Sum256([]byte(s))
		if sum[0]%2 == 1 {
			return ClaimRejected
		}
	}
	h := sha256.Sum256([]byte(s + c.ID + c.Version))
	_ = h
	return ClaimAccepted
}

// ContractDigest binds a contract revision (identity input, not semantics).
func ContractDigest(c ContractSpec) string {
	keys := make([]string, 0, len(c.Rules))
	for k := range c.Rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := c.ID + "/" + c.Version + "|"
	for _, k := range keys {
		h += k + "=" + c.Rules[k] + ";"
	}
	return DigestString(h)
}

// HeldOutSeeds and HeldOutRotations are reserved for the final gate (never
// used during development; declared here upfront).
var HeldOutSeeds = []uint64{9001, 9002, 9003}

var HeldOutRotations = []string{"w1-schema/v7-relaxed2", "w2-policy/v6-tightened"}

type splitRng struct{ s uint64 }

func newSplitRng(seed uint64) *splitRng { return &splitRng{s: seed | 1} }

func (r *splitRng) next() uint64 {
	r.s += 0x9E3779B97F4A7C15
	z := r.s
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *splitRng) bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.next() >> 33)
	}
	return b
}
