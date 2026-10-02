package realreplay

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Frozen fixtures, tasks and mutations. Changing anything in this file
// after the protocol freeze invalidates the assay.

const opencodeConfig = `{
  "$schema": "https://opencode.ai/config.json",
  "permission": {"task": "deny"}
}
`

// GitC0 is the shopcalc module at S0.
func GitC0() map[string]string {
	return map[string]string{
		"go.mod":        "module shopcalc\n\ngo 1.22\n",
		"opencode.json": opencodeConfig,
		"README.md":     "# shopcalc\n\nSmall pricing and formatting helpers.\n",
		"docs/NOTES.md": "# Notes\n\nv0.1: initial helpers.\n",
		"format/money.go": `package format

import "fmt"

// FormatUSD renders a dollar amount like "$1.50".
func FormatUSD(amount float64) string {
	return fmt.Sprintf("$%.2f", amount)
}
`,
		"format/money_test.go": `package format

import "testing"

func TestFormatUSD(t *testing.T) {
	if got := FormatUSD(1.5); got != "$1.50" {
		t.Fatalf("got %q", got)
	}
}
`,
		"pricing/discount.go": `package pricing

// Discount returns price reduced by pct percent (pct=25 takes 25% off).
func Discount(price, pct float64) float64 {
	return price * (1 - pct/100)
}
`,
		"pricing/discount_test.go": `package pricing

import "testing"

func TestDiscount(t *testing.T) {
	if got := Discount(100, 25); got != 75 {
		t.Fatalf("got %v", got)
	}
}
`,
	}
}

// GitMutations are the frozen concurrent commits (applied to main after
// the S0 agent run completes, before validation).
var GitMutations = map[string]map[string]string{
	// G1 late premise: Discount switches from percent to basis points.
	// A LineTotal written against v1 (passing percent) is wrong on v2.
	"G1": {
		"pricing/discount.go": `package pricing

// Discount returns price reduced by bps basis points
// (1 bp = 0.01%; bps=2500 takes 25% off).
func Discount(price, bps float64) float64 {
	return price * (1 - bps/10000)
}
`,
		"pricing/discount_test.go": `package pricing

import "testing"

func TestDiscount(t *testing.T) {
	if got := Discount(100, 2500); got != 75 {
		t.Fatalf("got %v", got)
	}
}
`,
	},
	// G2 early premise: FormatUSD gains thousands separators. Step 1's
	// accounting-style change must now also keep separators.
	"G2": {
		"format/money.go": `package format

import (
	"fmt"
	"strings"
)

// FormatUSD renders a dollar amount like "$1,234.50" (thousands
// separated).
func FormatUSD(amount float64) string {
	s := fmt.Sprintf("%.2f", amount)
	whole, frac := s[:len(s)-3], s[len(s)-3:]
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return "$" + b.String() + frac
}
`,
		"format/money_test.go": `package format

import "testing"

func TestFormatUSD(t *testing.T) {
	if got := FormatUSD(1.5); got != "$1.50" {
		t.Fatalf("got %q", got)
	}
	if got := FormatUSD(1234.5); got != "$1,234.50" {
		t.Fatalf("got %q", got)
	}
}
`,
	},
	// G3 unrelated: a docs file the task never needs.
	"G3": {
		"docs/NOTES.md": "# Notes\n\nv0.1: initial helpers.\nv0.2: release checklist added.\n",
	},
}

// TaskGit is the frozen Git task text (identical for every treatment).
const TaskGit = `This is a small Go module. Do two things, in this order:
1. In format/money.go, make FormatUSD render negative amounts in accounting style: FormatUSD(-1.5) must return "($1.50)". Positive amounts are unchanged. Add a test case for it in format/money_test.go.
2. Then add a new file pricing/line.go with a function ` + "`LineTotal(unitPrice float64, qty int, discountPct float64) float64`" + ` that returns the total for qty units after a percentage discount (discountPct=25 means 25% off), using the existing pricing.Discount function. Add a test for it in pricing/line_test.go.
Finally run ` + "`go test ./...`" + ` and make sure it passes. Do not commit. Reply with a one-line summary.`

// gitProbes are the task probes, keyed by the mutation that defines the
// final state ("" = S0 / unchanged format behavior).
func gitProbes(mutation string) map[string]string {
	neg := `	if got := FormatUSD(-1.5); got != "($1.50)" {
		t.Fatalf("FormatUSD(-1.5)=%q", got)
	}
	if got := FormatUSD(2); got != "$2.00" {
		t.Fatalf("FormatUSD(2)=%q", got)
	}
`
	if mutation == "G2" {
		neg += `	if got := FormatUSD(-1234.5); got != "($1,234.50)" {
		t.Fatalf("FormatUSD(-1234.5)=%q", got)
	}
`
	}
	return map[string]string{
		"format/zz_probe_test.go": "package format\n\nimport \"testing\"\n\nfunc TestProbeFormat(t *testing.T) {\n" + neg + "}\n",
		"pricing/zz_probe_test.go": `package pricing

import (
	"math"
	"testing"
)

func TestProbeLineTotal(t *testing.T) {
	if got := LineTotal(10, 2, 25); math.Abs(got-15) > 1e-9 {
		t.Fatalf("LineTotal(10,2,25)=%v want 15", got)
	}
}
`,
	}
}

// OracleGit runs the deterministic oracle on a committed tree: the full
// suite plus task probes for the final state. Returns pass and output.
func OracleGit(files map[string]string, mutation string) (bool, string) {
	dir, err := os.MkdirTemp("", "rre-oracle-*")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(dir)
	all := map[string]string{}
	for p, c := range files {
		all[p] = c
	}
	for p, c := range gitProbes(mutation) {
		all[p] = c
	}
	if err := WriteFiles(dir, all); err != nil {
		return false, err.Error()
	}
	cmd := exec.Command("go", "test", "-count=1", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// HTTPPort is fixed so the task text and system prompt are identical
// across treatments.
const HTTPAddr = "127.0.0.1:18741"

// HTTPS0 is the HTTP fixture at S0.
func HTTPS0() map[string]string {
	return map[string]string{
		"/customers/c42":    `{"id":"c42","name":"Acme Ltd","tier":"gold","country":"DE"}`,
		"/policy/discounts": `{"unit":"percent","tiers":{"gold":10,"silver":5,"bronze":0}}`,
		"/pricing/widget":   `{"sku":"widget","unit_price":40.0,"currency":"USD"}`,
		"/quotes/q1":        `{"status":"draft"}`,
	}
}

// HTTPMutations are the frozen concurrent writes.
var HTTPMutations = map[string]map[string]string{
	// H1 late premise: unit price changes; the quote total changes.
	"H1": {"/pricing/widget": `{"sku":"widget","unit_price":52.0,"currency":"USD"}`},
	// H2 early premise: gold discount changes; customer.md and quote change.
	"H2": {"/policy/discounts": `{"unit":"percent","tiers":{"gold":15,"silver":5,"bronze":0}}`},
}

// H1xSecond is the second change applied during the H1x replay.
var H1xSecond = map[string]string{"/pricing/widget": `{"sku":"widget","unit_price":61.0,"currency":"USD"}`}

// HTTPWorkspace is the (static) local workspace for the HTTP task.
func HTTPWorkspace() map[string]string {
	return map[string]string{
		"README.md":     "# quotes\n\nWorkspace for quote preparation.\n",
		"opencode.json": opencodeConfig,
	}
}

// TaskHTTP is the frozen HTTP task text.
const TaskHTTP = `An HTTP API is running at http://127.0.0.1:18741 . Do two things, in this order:
1. Fetch http://127.0.0.1:18741/customers/c42 and http://127.0.0.1:18741/policy/discounts. Write customer.md containing the customer's name, their tier, and the discount percent their tier receives.
2. Then fetch http://127.0.0.1:18741/pricing/widget and write quote.json containing exactly these fields: {"customer": <customer id>, "sku": "widget", "qty": 3, "unit_price": <unit price>, "discount_pct": <discount percent>, "total": <qty * unit_price * (1 - discount_pct/100), rounded to 2 decimals>}.
Do not modify any HTTP resource. Do not commit. Reply with a one-line summary.`

// HTTPExpect is the final-state oracle input derived from the authority.
type HTTPExpect struct {
	Name       string
	Tier       string
	UnitPrice  float64
	DiscountPc float64
}

// ExpectFrom reads the expected answer from authority state.
func ExpectFrom(res map[string]string) (HTTPExpect, error) {
	var c struct{ Name, Tier string }
	var p struct {
		Tiers map[string]float64 `json:"tiers"`
	}
	var w struct {
		UnitPrice float64 `json:"unit_price"`
	}
	if err := json.Unmarshal([]byte(res["/customers/c42"]), &c); err != nil {
		return HTTPExpect{}, err
	}
	if err := json.Unmarshal([]byte(res["/policy/discounts"]), &p); err != nil {
		return HTTPExpect{}, err
	}
	if err := json.Unmarshal([]byte(res["/pricing/widget"]), &w); err != nil {
		return HTTPExpect{}, err
	}
	return HTTPExpect{c.Name, c.Tier, w.UnitPrice, p.Tiers[c.Tier]}, nil
}

// OracleHTTP checks the workspace outputs against the expected answer.
func OracleHTTP(ws string, e HTTPExpect) (bool, string) {
	q, err := os.ReadFile(filepath.Join(ws, "quote.json"))
	if err != nil {
		return false, "quote.json missing"
	}
	var got map[string]interface{}
	if err := json.Unmarshal(q, &got); err != nil {
		return false, "quote.json invalid: " + err.Error()
	}
	f := func(k string) float64 { v, _ := got[k].(float64); return v }
	want := math.Round(3*e.UnitPrice*(1-e.DiscountPc/100)*100) / 100
	var bad []string
	if got["customer"] != "c42" {
		bad = append(bad, fmt.Sprintf("customer=%v", got["customer"]))
	}
	if got["sku"] != "widget" {
		bad = append(bad, fmt.Sprintf("sku=%v", got["sku"]))
	}
	if f("qty") != 3 {
		bad = append(bad, fmt.Sprintf("qty=%v", got["qty"]))
	}
	if f("unit_price") != e.UnitPrice {
		bad = append(bad, fmt.Sprintf("unit_price=%v want %v", got["unit_price"], e.UnitPrice))
	}
	if f("discount_pct") != e.DiscountPc {
		bad = append(bad, fmt.Sprintf("discount_pct=%v want %v", got["discount_pct"], e.DiscountPc))
	}
	if math.Abs(f("total")-want) > 0.005 {
		bad = append(bad, fmt.Sprintf("total=%v want %v", got["total"], want))
	}
	if len(got) != 6 {
		bad = append(bad, fmt.Sprintf("%d fields want 6", len(got)))
	}
	md, err := os.ReadFile(filepath.Join(ws, "customer.md"))
	if err != nil {
		bad = append(bad, "customer.md missing")
	} else {
		s := string(md)
		pct := fmt.Sprintf("%g", e.DiscountPc)
		if !strings.Contains(s, e.Name) || !strings.Contains(strings.ToLower(s), e.Tier) || !strings.Contains(s, pct) {
			bad = append(bad, "customer.md lacks name/tier/"+pct)
		}
	}
	if len(bad) > 0 {
		return false, strings.Join(bad, "; ")
	}
	return true, "ok"
}
