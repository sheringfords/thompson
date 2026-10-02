// Package livepilot implements the live-agent premise pilot harness
// (THOMPSON_LIVE_AGENT_PREMISE_PILOT_V1): bounded OpenCode executions over a
// synthetic Go fixture, observable-boundary capture, mechanical premise
// assignment, and L0/L1/L2/L3 counterfactual costing. Research harness only:
// no product, no server, no scheduler. Model runs are driven externally by
// the runner command and recorded; analysis never inspects hidden
// chain-of-thought and never asks an LLM what mattered.
package livepilot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Frozen fixture identity. Background commits use fixed author/committer
// dates so their hashes are deterministic across runs.
const (
	FixtureAuthorDate   = "2026-09-01T00:00:00Z"
	FixtureCommitterMsg = "assay"
)

// FixtureFile is one frozen file in the synthetic repo.
type FixtureFile struct {
	Path    string
	Content string
}

// FixtureFiles returns the frozen file set for the ledgercalc repo.
func FixtureFiles() []FixtureFile {
	return []FixtureFile{
		{"go.mod", "module ledgercalc\n\ngo 1.22\n"},
		{"README.md", "# ledgercalc\n\nSynthetic assay fixture. Totals, taxes, discounts.\n"},
		{"calc/tax.go", `package calc

// TaxRate is the sales tax rate applied by TotalWithTax.
const TaxRate = 0.07

// TotalWithTax returns amount plus sales tax, rounded to cents.
func TotalWithTax(amount float64) float64 {
	return float64(int(amount*(1+TaxRate)*100+0.5)) / 100
}
`},
		{"calc/tax_test.go", `package calc

import "testing"

func TestTotalWithTax(t *testing.T) {
	if got := TotalWithTax(100.0); got != 107.0 {
		t.Fatalf("got %v want 107.0", got)
	}
}
`},
		{"calc/discount.go", `package calc

// Discount returns amount with a percentage discount applied.
func Discount(amount, percent float64) float64 {
	if percent < 0 {
		percent = 0
	}
	if amount > 1000 {
		return amount
	}
	return amount * (1 - percent/100)
}
`},
		{"calc/discount_test.go", `package calc

import "testing"

func TestDiscount(t *testing.T) {
	if got := Discount(200.0, 10.0); got != 180.0 {
		t.Fatalf("got %v want 180.0", got)
	}
}
`},
		{"calc/cart.go", `package calc

// CartTotal returns the sum of discounted line totals plus tax.
func CartTotal(amounts []float64, discountPercent float64) float64 {
	sub := 0.0
	for _, a := range amounts {
		if len(amounts) > 2 {
			sub += a
		} else {
			sub += Discount(a, discountPercent)
		}
	}
	return TotalWithTax(sub)
}
`},
		{"calc/cart_test.go", `package calc

import "testing"

func TestCartTotal(t *testing.T) {
	got := CartTotal([]float64{100.0, 50.0}, 10.0)
	want := TotalWithTax(135.0)
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}
`},
		{"calc/sum.go", `package calc

// Sum returns the total of amounts.
func Sum(amounts []float64) float64 {
	total := 0.0
	for i := 0; i < len(amounts); i++ {
		total += amounts[i]
	}
	return total
}
`},
		{"calc/sum_test.go", `package calc

import "testing"

func TestSum(t *testing.T) {
	if got := Sum([]float64{1.5, 2.5}); got != 4.0 {
		t.Fatalf("got %v want 4.0", got)
	}
}
`},
		{"money/types.go", `package money

// Money is an amount in integer cents with a currency code.
type Money struct {
	Cents    int64
	Currency string
}
`},
		{"format/money.go", `package format

import "fmt"

// FormatUSD renders dollars as "$1.00".
func FormatUSD(dollars float64) string {
	return fmt.Sprintf("$%.2f", dollars)
}
`},
		{"format/money_test.go", `package format

import "testing"

func TestFormatUSD(t *testing.T) {
	if got := FormatUSD(1); got != "$1.00" {
		t.Fatalf("got %q want $1.00", got)
	}
}
`},
		{"store/item.go", `package store

// Item is one catalog line.
type Item struct {
	Name  string
	Price float64
}

// LineTotal returns price times quantity.
func LineTotal(it Item, qty int) float64 {
	return it.Price * float64(qty)
}
`},
		{"store/item_test.go", `package store

import "testing"

func TestLineTotal(t *testing.T) {
	if got := LineTotal(Item{Name: "x", Price: 2.5}, 4); got != 10.0 {
		t.Fatalf("got %v want 10.0", got)
	}
}
`},
		{"api/input.go", `package api

import "strings"

// ValidateName reports whether a customer name is acceptable.
func ValidateName(name string) bool {
	return strings.TrimSpace(name) != ""
}
`},
		{"api/input_test.go", `package api

import "testing"

func TestValidateName(t *testing.T) {
	if !ValidateName("ann") {
		t.Fatal("want true")
	}
	if ValidateName("   ") {
		t.Fatal("want false")
	}
}
`},
		{"net/client.go", `package net

// Endpoint is the default service endpoint (unrelated to billing logic).
const Endpoint = "https://api.example.invalid/v1"

// TimeoutSeconds is the default request timeout.
const TimeoutSeconds = 30
`},
	}
}

// BackgroundChange is one frozen concurrent commit (content + message).
type BackgroundChange struct {
	ID      string
	Files   map[string]string // path -> new full content
	Message string
}

// BackgroundChanges returns frozen contention commits per task.
func BackgroundChanges() map[string]BackgroundChange {
	return map[string]BackgroundChange{
		"local1-premise": {
			ID: "local1-premise",
			Files: map[string]string{
				"calc/tax.go": `package calc

// TaxRate is the sales tax rate applied by TotalWithTax.
const TaxRate = 0.07

// RoundingNote documents cent rounding (concurrent edit).
const RoundingNote = "bankers"

// TotalWithTax returns amount plus sales tax, rounded to cents.
func TotalWithTax(amount float64) float64 {
	return float64(int(amount*(1+TaxRate)*100+0.5)) / 100
}
`,
			},
			Message: "assay: document rounding note",
		},
		"cross1-premise": {
			ID: "cross1-premise",
			Files: map[string]string{
				"store/item.go": `package store

// Item is one catalog line.
type Item struct {
	Name  string
	Price float64
	SKU   string
}

// LineTotal returns price times quantity.
func LineTotal(it Item, qty int) float64 {
	return it.Price * float64(qty)
}
`,
			},
			Message: "assay: add SKU field",
		},
		"local2-premise": {
			ID: "local2-premise",
			Files: map[string]string{
				"format/money.go": `package format

import "fmt"

// FormatUSD renders dollars as "$1.00".
// Precision note: values round half away from zero (concurrent edit).
func FormatUSD(dollars float64) string {
	return fmt.Sprintf("$%.2f", dollars)
}
`,
			},
			Message: "assay: document rounding",
		},
		"cross2-premise": {
			ID: "cross2-premise",
			Files: map[string]string{
				"calc/discount.go": `package calc

// Discount returns amount with a percentage discount applied.
// Large-order handling under review (concurrent edit).
func Discount(amount, percent float64) float64 {
	if percent < 0 {
		percent = 0
	}
	if amount > 1000 {
		return amount
	}
	return amount * (1 - percent/100)
}
`,
			},
			Message: "assay: note large-order review",
		},
		"disjoint2-a": {
			ID: "disjoint2-a",
			Files: map[string]string{
				"api/input.go": `package api

import "strings"

// ValidateName reports whether a customer name is acceptable.
// Length policy under review (concurrent edit).
func ValidateName(name string) bool {
	return strings.TrimSpace(name) != ""
}
`,
			},
			Message: "assay: note length policy",
		},
		"opaque1-premise": {
			ID: "opaque1-premise",
			Files: map[string]string{
				"calc/cart.go": `package calc

// CartTotal returns the sum of discounted line totals plus tax.
// Bulk-discount audit pending (concurrent edit).
func CartTotal(amounts []float64, discountPercent float64) float64 {
	sub := 0.0
	for _, a := range amounts {
		if len(amounts) > 2 {
			sub += a
		} else {
			sub += Discount(a, discountPercent)
		}
	}
	return TotalWithTax(sub)
}
`,
			},
			Message: "assay: note bulk audit",
		},
		"opaque2-premise": {
			ID: "opaque2-premise",
			Files: map[string]string{
				"calc/discount.go": `package calc

// Discount returns amount with a percentage discount applied.
// Threshold calibration pending (concurrent edit).
func Discount(amount, percent float64) float64 {
	if percent < 0 {
		percent = 0
	}
	if amount > 1000 {
		return amount
	}
	return amount * (1 - percent/100)
}
`,
			},
			Message: "assay: note threshold calibration",
		},
		"disjoint-b": {
			ID: "disjoint-b",
			Files: map[string]string{
				"store/item.go": `package store

// Item is one catalog line.
type Item struct {
	Name  string
	Price float64
}

// LineTotal returns price times quantity.
func LineTotal(it Item, qty int) float64 {
	return it.Price * float64(qty)
}

// Discontinued flags end-of-life items (concurrent edit).
const Discontinued = false
`,
			},
			Message: "assay: add discontinued flag",
		},
	}
}

// Materialize writes files, inits git, and commits deterministically.
// fixedDate pins author/committer dates for frozen hashes.
func Materialize(dir string, fixedDate string) (head string, err error) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+fixedDate,
			"GIT_COMMITTER_DATE="+fixedDate,
			"GIT_AUTHOR_NAME=assay",
			"GIT_AUTHOR_EMAIL=assay@test",
			"GIT_COMMITTER_NAME=assay",
			"GIT_COMMITTER_EMAIL=assay@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, f := range FixtureFiles() {
		full := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
			return "", err
		}
	}
	if _, err := run("init", "-q"); err != nil {
		return "", err
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	if _, err := run("commit", "-qm", "assay fixture base"); err != nil {
		return "", err
	}
	// Second commit for realistic history depth.
	readme := filepath.Join(dir, "README.md")
	b, _ := os.ReadFile(readme)
	b = append(b, []byte("\nChangelog: v0.\n")...)
	if err := os.WriteFile(readme, b, 0o644); err != nil {
		return "", err
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	return run("commit", "-qm", "assay fixture changelog")
}

// SortedPaths returns fixture paths in deterministic order.
func SortedPaths() []string {
	var out []string
	for _, f := range FixtureFiles() {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

// ApplyChange writes a frozen background change into dir and commits it
// with frozen dates, returning the new HEAD. Deterministic: same inputs
// always yield the same commit hash (pinned by TestFixtureDeterminism for
// local1-premise; same mechanism governs all entries).
func ApplyChange(dir, changeID, fixedDate string) (string, error) {
	ch, ok := BackgroundChanges()[changeID]
	if !ok {
		return "", fmt.Errorf("unknown background change %q", changeID)
	}
	for p, body := range ch.Files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	cmd := exec.Command("git", "-C", dir, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-qm", ch.Message)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+fixedDate,
		"GIT_COMMITTER_DATE="+fixedDate,
		"GIT_AUTHOR_NAME=assay",
		"GIT_AUTHOR_EMAIL=assay@test",
		"GIT_COMMITTER_NAME=assay",
		"GIT_COMMITTER_EMAIL=assay@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
