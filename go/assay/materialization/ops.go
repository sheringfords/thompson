package materialization

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// ops.go: deterministic operations over actual record bytes. All cost is real
// CPU (hashing, parsing, validation scans); no sleeps, no modeled costs.
// CPU time is MEASURED per call site by the runners.

// hashRounds scales per-op CPU. Calibrated so a record op costs tens of µs.
const hashRounds = 48

func burn(data []byte, rounds int) []byte {
	h := sha256.New()
	out := data
	for i := 0; i < rounds; i++ {
		h.Reset()
		h.Write(out)
		h.Write([]byte{byte(i), byte(i >> 8)})
		out = h.Sum(nil)
	}
	return out
}

// --- record-local ops (pure functions over bytes) ---

// ValidateOp checks field presence/format and binds the record digest.
// The burn digest feeds the output so the work cannot be eliminated.
func ValidateOp(rec Record) []byte {
	if rec.ID == "" || rec.Name == "" || rec.Category == "" || rec.Amount < 0 {
		return []byte("INVALID")
	}
	d := RecordDigest(rec)
	w := burn([]byte(d), hashRounds)
	return []byte("valid:" + reuse.DigestString(string(w)+d))
}

// NormalizeOp canonicalizes per schema (schema/v4 appends a derived field).
func NormalizeOp(rec Record, schema string) []byte {
	canon := fmt.Sprintf("%s|%s|%d|%s|%s", rec.ID,
		strings.ToLower(strings.TrimSpace(rec.Name)), rec.Amount,
		strings.ToLower(rec.Category), strings.TrimSpace(rec.Text))
	if schema == "schema/v4" {
		canon += "|v4extra:" + fmt.Sprint(rec.Amount%97)
	}
	return []byte("norm:" + reuse.DigestString(canon) + ":" + canon[:min(64, len(canon))])
}

// ExtractOp projects the fixed schema fields.
func ExtractOp(norm []byte, schema string) []byte {
	parts := strings.Split(string(norm), "|")
	proj := strings.Join(parts, ";")
	if schema == "schema/v4" {
		proj += ";v4=1"
	}
	return []byte("ext:" + reuse.DigestString(proj) + ":" + proj[:min(48, len(proj))])
}

// VerifyOp re-derives extraction checks (real per-record validation work).
func VerifyOp(rec Record, ext []byte) []byte {
	d := RecordDigest(rec)
	chk := burn(append([]byte(d), ext...), hashRounds)
	return []byte("ok:" + reuse.DigestString(string(chk)+d))
}

// EnrichOp joins the shared taxonomy (real parse + rate application).
func EnrichOp(ext []byte, taxonomy string) []byte {
	tax := TaxonomyDigest(taxonomy)
	mixed := burn(append(append([]byte{}, ext...), []byte(tax)...), hashRounds)
	return []byte("enr:" + reuse.DigestString(string(mixed)+tax))
}

// AggregateOp folds all enriched records (sorted) with the global param.
func AggregateOp(enriched map[string][]byte, aggParam string) []byte {
	ids := make([]string, 0, len(enriched))
	for id := range enriched {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	h.Write([]byte(aggParam + "|"))
	for _, id := range ids {
		sum := sha256.Sum256(enriched[id])
		h.Write(sum[:])
		h.Write([]byte(id + ";"))
	}
	return []byte("agg:" + fmt.Sprintf("%x", h.Sum(nil)))
}

// ReportOp formats the aggregate (staged path).
func ReportOp(agg []byte) []byte {
	return []byte("report:" + reuse.DigestString("staged|"+string(agg)))
}

// DirectOp folds + formats in one expensive step (alternative physical plan).
func DirectOp(enriched map[string][]byte, aggParam string) []byte {
	agg := AggregateOp(enriched, aggParam) // real repeated fold work
	extra := burn(agg, hashRounds*2)
	return []byte("report:" + reuse.DigestString("direct|"+string(extra)))
}

// AttestOp is the terminal verification: full revalidation scan over the
// report plus a digest rollup of per-record verify outputs (O(N) real work).
func AttestOp(report []byte, verifyRollup string) []byte {
	scan := burn(append(append([]byte{}, report...), []byte(verifyRollup)...), hashRounds)
	return []byte("attest:" + reuse.DigestString(string(scan)+verifyRollup))
}

// VerifyRollup binds all per-record verify outputs (sorted).
func VerifyRollup(verify map[string][]byte) string {
	ids := make([]string, 0, len(verify))
	for id := range verify {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write(verify[id])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ContractFor binds op verifier contracts (v1 default; runners rotate).
func ContractFor(op, version string) string {
	return reuse.DigestString("mat-contract:" + op + "/" + version)
}
