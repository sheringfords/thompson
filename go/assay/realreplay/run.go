package realreplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrCap is returned when the frozen invocation cap would be exceeded.
var ErrCap = errors.New("invocation cap reached")

// Runner executes treatments and records everything under Out.
type Runner struct {
	Work          string        // scratch root for authorities and workspaces
	Out           string        // run records (committed evidence)
	Cap           int           // frozen total opencode invocation cap
	Timeout       time.Duration // per invocation
	ResumeMessage string        // frozen resume trigger for R2 continuations
}

// RunRec is one recorded agent segment.
type RunRec struct {
	Label  string
	Dir    string
	Inv    Invocation
	Stream *Stream
	Export string
}

type ledgerEntry struct {
	Event   string `json:"event"`
	Label   string `json:"label"`
	At      string `json:"at"`
	Session string `json:"session,omitempty"`
	Exit    int    `json:"exit,omitempty"`
	Calls   int    `json:"model_calls,omitempty"`
	WallMS  int64  `json:"wall_ms,omitempty"`
	Note    string `json:"note,omitempty"`
}

func (r *Runner) ledger() string { return filepath.Join(r.Out, "invocations.jsonl") }

// Used counts invocation starts recorded in the ledger.
func (r *Runner) Used() (int, error) {
	b, err := os.ReadFile(r.ledger())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"event":"start"`) {
			n++
		}
	}
	return n, nil
}

func (r *Runner) appendLedger(e ledgerEntry) error {
	e.At = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(r.ledger(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// invoke runs one opencode process (counted before launch) and records
// stream, parsed calls and session export.
func (r *Runner) invoke(ctx context.Context, label, ws, msg, session string) (*RunRec, error) {
	used, err := r.Used()
	if err != nil {
		return nil, err
	}
	if used >= r.Cap {
		return nil, fmt.Errorf("%w (%d/%d) before %s", ErrCap, used, r.Cap, label)
	}
	dir := filepath.Join(r.Out, "runs", label)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := r.appendLedger(ledgerEntry{Event: "start", Label: label, Session: session}); err != nil {
		return nil, err
	}
	inv, err := RunAgent(ctx, ws, msg, session, filepath.Join(dir, "stream.jsonl"), r.Timeout)
	inv.Label = label
	if err != nil {
		return nil, err
	}
	st, err := ParseStream(inv.StreamOut)
	if err != nil {
		return nil, err
	}
	rec := &RunRec{Label: label, Dir: dir, Inv: inv, Stream: st}
	if st.SessionID != "" {
		rec.Export = filepath.Join(dir, "export.json")
		if err := ExportSession(ws, st.SessionID, rec.Export); err != nil {
			return nil, err
		}
		if err := fillUsageFromExport(st, rec.Export); err != nil {
			return nil, err
		}
	}
	_ = writeJSON(filepath.Join(dir, "calls.json"), st)
	_ = writeJSON(filepath.Join(dir, "invocation.json"), inv)
	err = r.appendLedger(ledgerEntry{Event: "end", Label: label, Session: st.SessionID, Exit: inv.Exit, Calls: len(st.Calls), WallMS: inv.WallMS})
	if inv.Exit != 0 || inv.TimedOut || len(st.Errors) > 0 {
		return rec, fmt.Errorf("invocation %s failed: exit=%d timeout=%v errors=%v", label, inv.Exit, inv.TimedOut, st.Errors)
	}
	return rec, err
}

// exportAssistantIDs returns assistant message IDs of an export, in order.
func exportAssistantIDs(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var exp struct {
		Messages []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range exp.Messages {
		if m.Type == "assistant" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// checkAlignment verifies that stream calls and export assistant messages
// are the same sequence (one assistant message per model call).
func checkAlignment(rec *RunRec, offset int) error {
	ids, err := exportAssistantIDs(rec.Export)
	if err != nil {
		return err
	}
	if len(ids) != offset+len(rec.Stream.Calls) {
		return fmt.Errorf("%s: export has %d assistant messages, stream %d calls (+%d preserved)", rec.Label, len(ids), len(rec.Stream.Calls), offset)
	}
	for i, c := range rec.Stream.Calls {
		if ids[offset+i] != c.MessageID || c.Steps != 1 {
			return fmt.Errorf("%s: call %d message %s steps=%d does not align with export %s", rec.Label, i, c.MessageID, c.Steps, ids[offset+i])
		}
	}
	return nil
}

// setWorkspace makes ws an exact checkout of commit with files overlaid
// (files is the complete desired file set).
func setWorkspace(a *Authority, ws, commit string, files map[string]string) error {
	if err := a.Materialize(ws, commit); err != nil {
		return err
	}
	cur, err := ReadWorkspace(ws)
	if err != nil {
		return err
	}
	for p := range cur {
		if _, ok := files[p]; !ok {
			if err := os.Remove(filepath.Join(ws, filepath.FromSlash(p))); err != nil {
				return err
			}
		}
	}
	return WriteFiles(ws, files)
}

// classifyDiscards labels each discarded call k (k >= from) by the stale
// premises produced before it.
func classifyDiscards(prem []Premise, v Validation, from, n int) (trueDep, opaque, none int) {
	staleAt := map[int][2]bool{} // call -> {precise, opaque}
	for _, p := range v.Stale {
		f := staleAt[p.Call]
		if p.Why == "" {
			f[0] = true
		} else {
			f[1] = true
		}
		staleAt[p.Call] = f
	}
	for _, p := range v.Unknown {
		f := staleAt[p.Call]
		f[1] = true
		staleAt[p.Call] = f
	}
	for k := from; k < n; k++ {
		precise, opq := false, false
		for c, f := range staleAt {
			if c < k {
				precise = precise || f[0]
				opq = opq || f[1]
			}
		}
		switch {
		case precise:
			trueDep++
		case opq:
			opaque++
		default:
			none++
		}
	}
	return
}

func countUnknown(ps []Premise) int {
	n := 0
	for _, p := range ps {
		if p.Kind == KindUnknown {
			n++
		}
	}
	return n
}

func httpEtags(h *HTTPAuthority) map[string]string {
	m := map[string]string{}
	for _, p := range h.Paths() {
		_, t, _ := h.Current(p)
		m[p] = t
	}
	return m
}

func retagged(ids []string, tag string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id[:len(id)-4]+tag] = true
	}
	return m
}

// resume builds a continuation keeping `keep` assistant messages of the
// export, imports it and resumes it for real.
func (r *Runner) resume(ctx context.Context, label, ws, export string, keep int, tag string) (*RunRec, ContinuationStats, error) {
	dir := filepath.Join(r.Out, "runs", label)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, ContinuationStats{}, err
	}
	cont := filepath.Join(dir, "continuation.json")
	st, err := BuildContinuation(export, cont, ws, tag, keep)
	if err != nil {
		return nil, st, err
	}
	_ = writeJSON(filepath.Join(dir, "continuation-stats.json"), st)
	if err := ImportSession(ws, cont); err != nil {
		return nil, st, err
	}
	rec, err := r.invoke(ctx, label, ws, r.ResumeMessage, st.NewSession)
	if err != nil {
		return rec, st, err
	}
	// Preserved messages must not have been re-executed: no new call may
	// carry a preserved message ID.
	kept := retagged(st.KeptMessageIDs, tag)
	for _, c := range rec.Stream.Calls {
		if kept[c.MessageID] {
			return rec, st, fmt.Errorf("%s: preserved message %s was re-executed", label, c.MessageID)
		}
	}
	return rec, st, checkAlignment(rec, keep)
}

// Outcome is everything recorded for one scenario.
type Outcome struct {
	Scenario     Scenario            `json:"scenario"`
	Gates        Gates               `json:"gates"`
	S0Valid      Validation          `json:"s0_validation_against_s1"`
	R2Final      *Validation         `json:"r2_final_validation,omitempty"`
	R0Final      *Validation         `json:"r0_final_validation,omitempty"`
	Rounds       []Validation        `json:"r2_round_validations,omitempty"`
	OracleOut    map[string]string   `json:"oracle_output"`
	Continuation []ContinuationStats `json:"continuations,omitempty"`
}

func sortedKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// fillUsageFromExport takes each call's usage from OpenCode's stored
// assistant message (the stream omits the final step_finish when the
// process exits). Where the stream also reported usage, both must agree
// exactly; a disagreement aborts the run as a telemetry failure.
func fillUsageFromExport(st *Stream, export string) error {
	b, err := os.ReadFile(export)
	if err != nil {
		return err
	}
	var exp struct {
		Messages []struct {
			ID     string                 `json:"id"`
			Type   string                 `json:"type"`
			Finish string                 `json:"finish"`
			Tokens map[string]interface{} `json:"tokens"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		return err
	}
	byID := map[string]int{}
	for i, m := range exp.Messages {
		byID[m.ID] = i
	}
	for _, c := range st.Calls {
		i, ok := byID[c.MessageID]
		if !ok || exp.Messages[i].Tokens == nil {
			continue
		}
		tk := exp.Messages[i].Tokens
		var t Tokens
		t.Input, _ = num(tk, "input")
		t.Output, _ = num(tk, "output")
		t.Reasoning, t.ReasoningExposed = num(tk, "reasoning")
		if cache, ok := tk["cache"].(map[string]interface{}); ok {
			t.CacheRead, _ = num(cache, "read")
			t.CacheWrite, _ = num(cache, "write")
		}
		if c.Usage && (t.Input != c.Tokens.Input || t.Output != c.Tokens.Output || t.Reasoning != c.Tokens.Reasoning || t.CacheRead != c.Tokens.CacheRead || t.CacheWrite != c.Tokens.CacheWrite) {
			return fmt.Errorf("usage mismatch for %s: stream %+v export %+v", c.MessageID, c.Tokens, t)
		}
		c.Tokens, c.Usage = t, true
		if c.Finish == "" {
			c.Finish = exp.Messages[i].Finish
		}
	}
	return nil
}
