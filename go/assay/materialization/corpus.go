// Package materialization implements the materialized-execution assay
// (THOMPSON_MATERIALIZED_EXECUTION_ASSAY_V1): a realistic recurring
// document/data workload comparing full recomputation (A), conventional
// exact per-step cache (B), verification-memory cache (B+ sensitivity),
// verified dependency-aware materialization (C) and C plus physical-plan
// selection (D). ExecutionKey/VerifiedArtifact V1 are reused unchanged.
// Research harness only: no production code, no customer data, generated
// synthetic records only.
package materialization

import (
	"fmt"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// corpus.go: generated versioned document corpus and mutation series.

// Record is one synthetic source document (generated, redistributable).
type Record struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Amount   int64  `json:"amount"`
	Category string `json:"category"`
	Text     string `json:"text"`
}

// Corpus is one versioned source state.
type Corpus struct {
	Version  string
	Records  []Record // sorted by ID
	Schema   string   // schema/config label, e.g. "schema/v3"
	Taxonomy string   // shared enrichment table label, e.g. "tax/v1"
	AggParam string   // global aggregate dependency, e.g. "topk/10"
}

// GenCorpus builds N deterministic records from seed (splitmix64).
func GenCorpus(seed uint64, n int) Corpus {
	r := newRng(seed | 1)
	cats := []string{"a", "b", "c", "d"}
	recs := make([]Record, n)
	for i := 0; i < n; i++ {
		recs[i] = Record{
			ID:       fmt.Sprintf("r%06d", i),
			Name:     fmt.Sprintf("name-%x", r.bytes(6)),
			Amount:   int64(r.next() % 100000),
			Category: cats[r.next()%4],
			Text:     fmt.Sprintf("body-%x-%x", r.bytes(48), r.bytes(48)),
		}
	}
	return Corpus{Version: "v0", Records: recs, Schema: "schema/v3",
		Taxonomy: "tax/v1", AggParam: "topk/10"}
}

// WithVersion returns a mutated copy per the frozen mutation series.
func (c Corpus) WithVersion(mut Mutation) Corpus {
	out := c
	out.Records = append([]Record(nil), c.Records...)
	out.Version = mut.ID
	mut.Apply(&out)
	return out
}

// Mutation is one frozen corpus change.
type Mutation struct {
	ID    string
	Apply func(*Corpus)
}

// MutationSeries lists the frozen series (Phase 2). Fractions mutate the
// first k records' Amount (deterministic, sparse, record-local).
func MutationSeries() []Mutation {
	frac := func(pct int) Mutation {
		return Mutation{ID: fmt.Sprintf("chg-%dpc", pct), Apply: func(c *Corpus) {
			k := len(c.Records) * pct / 100
			for i := 0; i < k; i++ {
				c.Records[i].Amount += 1000000 + int64(i)
				c.Records[i].Text += "|amended"
			}
		}}
	}
	return []Mutation{
		{ID: "no-change", Apply: func(*Corpus) {}},
		frac(1),
		frac(5),
		frac(20),
		{ID: "schema-change", Apply: func(c *Corpus) { c.Schema = "schema/v4" }},
		{ID: "verifier-change", Apply: func(*Corpus) { /* contract rotates at runner level */ }},
		{ID: "enrichment-change", Apply: func(c *Corpus) { c.Taxonomy = "tax/v2" }},
		{ID: "aggregate-change", Apply: func(c *Corpus) { c.AggParam = "topk/25" }},
		{ID: "revocation", Apply: func(*Corpus) { /* revokes prior evidence at runner level */ }},
		{ID: "full-replace", Apply: func(c *Corpus) {
			for i := range c.Records {
				c.Records[i].Amount += 7
				c.Records[i].Text += "|r"
			}
		}},
	}
}

// RecordDigest digests one record canonically.
func RecordDigest(r Record) string {
	return reuse.DigestString(fmt.Sprintf("%s|%s|%d|%s|%s", r.ID, r.Name, r.Amount, r.Category, r.Text))
}

// TaxonomyDigest digests the shared enrichment table label + fixed rows.
func TaxonomyDigest(label string) string {
	rows := map[string]string{"tax/v1": "a:1,b:2,c:3,d:4", "tax/v2": "a:1,b:2,c:5,d:4"}
	body, ok := rows[label]
	if !ok {
		body = "unknown:" + label
	}
	return reuse.DigestString(label + "|" + body)
}

type rng struct{ s uint64 }

func newRng(seed uint64) *rng { return &rng{s: seed} }

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

var _ = sort.Strings
