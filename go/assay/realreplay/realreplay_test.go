package realreplay

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newGit(t *testing.T) (*Authority, string, string) {
	t.Helper()
	dir := t.TempDir()
	a, c0, err := NewAuthority(filepath.Join(dir, "auth"), GitC0())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "ws")
	if err := a.Materialize(ws, c0); err != nil {
		t.Fatal(err)
	}
	return a, c0, ws
}

func readCall(ws string, tools ...ToolUse) *Call { return &Call{Tools: tools} }

func tool(name string, in map[string]interface{}) ToolUse { return ToolUse{Tool: name, Input: in} }

func TestFixtureDeterministic(t *testing.T) {
	_, c0a, _ := newGit(t)
	_, c0b, _ := newGit(t)
	if c0a != c0b {
		t.Fatalf("C0 not deterministic: %s %s", c0a, c0b)
	}
}

func TestGitPremisesAndValidation(t *testing.T) {
	a, c0, ws := newGit(t)
	w := &World{Workspace: ws, Git: a, Base: c0}
	calls := []*Call{
		readCall(ws, tool("read", map[string]interface{}{"path": ws})),                                       // root listing
		readCall(ws, tool("read", map[string]interface{}{"path": filepath.Join(ws, "format/money.go")})),     // format
		readCall(ws, tool("glob", map[string]interface{}{"pattern": "pricing/*.go"})),                        // names
		readCall(ws, tool("read", map[string]interface{}{"path": filepath.Join(ws, "pricing/discount.go")})), // discount
		readCall(ws, tool("shell", map[string]interface{}{"command": "go test ./..."})),                      // opaque
		readCall(ws, tool("todowrite", nil)),
	}
	ps, err := w.Premises(calls, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := Validate(ps, a, nil); v.FirstStale != -1 {
		t.Fatalf("fresh premises reported stale: %+v", v)
	}
	if _, err := a.ConcurrentCommit(GitMutations["G1"], "G1"); err != nil {
		t.Fatal(err)
	}
	v, err := Validate(ps, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Root listing, format read and names projection survive a content
	// change; the discount blob and the opaque shell do not.
	if v.FirstStale != 3 {
		t.Fatalf("first stale = %d, want 3; stale=%+v", v.FirstStale, v.Stale)
	}
	keys := map[string]bool{}
	for _, p := range v.Stale {
		keys[p.Key] = true
	}
	if !keys["pricing/discount.go"] || !keys[""] || len(keys) != 2 {
		t.Fatalf("stale keys %v", keys)
	}
}

func TestUnrelatedChangeOnlyBreaksOpaque(t *testing.T) {
	a, c0, ws := newGit(t)
	w := &World{Workspace: ws, Git: a, Base: c0}
	ps, _ := w.Premises([]*Call{
		readCall(ws, tool("read", map[string]interface{}{"path": filepath.Join(ws, "pricing/discount.go")})),
		readCall(ws, tool("bash", map[string]interface{}{"command": "go test ./..."})),
	}, 0)
	a.ConcurrentCommit(GitMutations["G3"], "G3")
	v, _ := Validate(ps, a, nil)
	if v.FirstStale != 1 || len(v.Stale) != 1 || v.Stale[0].Why == "" {
		t.Fatalf("want only the opaque shell premise stale: %+v", v)
	}
}

func TestNamesWitnessTracksEntriesNotContent(t *testing.T) {
	a, c0, _ := newGit(t)
	n0, _ := a.NamesWitness(c0, "pricing", false)
	c1, _ := a.ConcurrentCommit(GitMutations["G1"], "content")
	n1, _ := a.NamesWitness(c1, "pricing", false)
	if n0 != n1 {
		t.Fatal("content change altered names witness")
	}
	c2, _ := a.ConcurrentCommit(map[string]string{"pricing/new.go": "package pricing\n"}, "add")
	n2, _ := a.NamesWitness(c2, "pricing", false)
	if n2 == n1 {
		t.Fatal("added entry did not alter names witness")
	}
}

func TestUnknownToolNeverValidates(t *testing.T) {
	a, c0, ws := newGit(t)
	w := &World{Workspace: ws, Git: a, Base: c0}
	ps, _ := w.Premises([]*Call{readCall(ws, tool("task", nil)), readCall(ws, tool("read", map[string]interface{}{"path": "/etc/hosts"}))}, 0)
	v, _ := Validate(ps, a, nil)
	if len(v.Unknown) != 2 || v.FirstStale != 0 {
		t.Fatalf("unknown premises must be stale: %+v", v)
	}
}

func TestWriteSetPinsBaseBlob(t *testing.T) {
	a, c0, ws := newGit(t)
	w := &World{Workspace: ws, Git: a, Base: c0}
	ps, _ := w.Premises([]*Call{readCall(ws, tool("write", map[string]interface{}{"path": filepath.Join(ws, "format/money.go")}))}, 0)
	a.ConcurrentCommit(GitMutations["G2"], "G2")
	if v, _ := Validate(ps, a, nil); len(v.Stale) != 1 {
		t.Fatalf("write over a concurrently changed file must be stale: %+v", v)
	}
}

func TestGitCASRejectsMovedMain(t *testing.T) {
	a, c0, ws := newGit(t)
	if _, err := a.ConcurrentCommit(GitMutations["G3"], "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CommitWorkspace(ws, c0, "stale parent"); err == nil {
		t.Fatal("CAS accepted a commit against a moved main")
	}
	head, _ := a.Main()
	if _, err := a.CommitWorkspace(ws, head, "fresh parent"); err != nil {
		t.Fatalf("CAS rejected a fresh commit: %v", err)
	}
}

func TestHTTPAuthority(t *testing.T) {
	h, err := NewHTTPAuthority("127.0.0.1:0", HTTPS0())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	resp, err := http.Get("http://" + h.Addr + "/pricing/widget")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, tag, _ := h.Current("/pricing/widget")
	if resp.Header.Get("ETag") != tag || len(h.Log()) != 1 || h.Log()[0].ETag != tag {
		t.Fatalf("etag/log mismatch")
	}
	w := &World{HTTP: h, HTTPLog: h.Log()}
	at := h.Log()[0].AtMS
	ps, _ := w.httpPremise(ToolUse{Tool: "webfetch", Input: map[string]interface{}{"url": "http://" + h.Addr + "/pricing/widget"}, StartMS: at, EndMS: at})
	if len(ps) != 1 || ps[0].Kind != KindHTTP || ps[0].Witness != tag {
		t.Fatalf("premise %+v", ps)
	}
	ps, _ = w.httpPremise(ToolUse{Tool: "webfetch", Input: map[string]interface{}{"url": "http://" + h.Addr + "/policy/discounts"}, StartMS: at, EndMS: at})
	if ps[0].Kind != KindUnknown {
		t.Fatal("unlogged fetch must be unknown")
	}
	_, qTag, _ := h.Current("/quotes/q1")
	if code, _ := ConditionalPut(h.Addr, "/quotes/q1", `"wrong"`, "{}", nil); code != http.StatusPreconditionFailed {
		t.Fatalf("wrong If-Match accepted: %d", code)
	}
	if code, _ := ConditionalPut(h.Addr, "/quotes/q1", qTag, "{}", map[string]string{"/pricing/widget": tag}); code != http.StatusOK {
		t.Fatalf("valid conditional put rejected: %d", code)
	}
}

func TestCommitRaceProbesAllRejected(t *testing.T) {
	ps, err := CommitRaceProbes(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 {
		t.Fatalf("probes %+v", ps)
	}
	for _, p := range ps {
		if !p.Rejected {
			t.Fatalf("race probe accepted: %+v", p)
		}
	}
}

func TestBuildContinuationObservableOnly(t *testing.T) {
	exp := map[string]interface{}{
		"info": map[string]interface{}{"id": "ses_abcdefgh0001", "location": map[string]interface{}{"directory": "/old"}, "title": "t"},
		"messages": []interface{}{
			map[string]interface{}{"id": "msg_u0000001", "type": "user", "text": "task"},
			map[string]interface{}{"id": "msg_a0000001", "type": "assistant", "content": []interface{}{
				map[string]interface{}{"type": "reasoning", "state": map[string]interface{}{"reasoningEncryptedContent": "SECRET"}},
				map[string]interface{}{"type": "tool", "providerState": map[string]interface{}{"itemId": "fc_1"}, "state": map[string]interface{}{"output": "ok"}},
			}},
			map[string]interface{}{"id": "msg_a0000002", "type": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "stale"}}},
		},
	}
	dir := t.TempDir()
	b, _ := json.Marshal(exp)
	in := filepath.Join(dir, "exp.json")
	os.WriteFile(in, b, 0o644)
	out := filepath.Join(dir, "cont.json")
	st, err := BuildContinuation(in, out, "/ws", "Ab01", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	s := string(got)
	if strings.Contains(s, "SECRET") || strings.Contains(s, "reasoning") || strings.Contains(s, "providerState") || strings.Contains(s, "stale") {
		t.Fatalf("hidden or stale content survived: %s", s)
	}
	if st.KeptAssistant != 1 || st.DroppedAssistant != 1 || st.NewSession != "ses_abcdefghAb01" ||
		!strings.Contains(s, "msg_u000Ab01") || !strings.Contains(s, "msg_a000Ab01") || !strings.Contains(s, `"/ws"`) {
		t.Fatalf("stats %+v\n%s", st, s)
	}
}

func TestRebase(t *testing.T) {
	old := map[string]string{"a": "1", "b": "1", "c": "1"}
	nw := map[string]string{"a": "2", "b": "1", "c": "1"}
	got, conf := Rebase(old, nw, map[string]string{"a": "1", "b": "X", "d": "new"})
	if len(conf) != 0 || got["a"] != "2" || got["b"] != "X" || got["d"] != "new" || got["c"] != "" {
		t.Fatalf("rebase %v %v", got, conf)
	}
	_, conf = Rebase(old, nw, map[string]string{"a": "agent", "b": "1", "c": "1"})
	if len(conf) != 1 {
		t.Fatal("agent edit of concurrently changed file must conflict")
	}
}

const lineV1 = "package pricing\n\nfunc LineTotal(unitPrice float64, qty int, discountPct float64) float64 {\n\treturn Discount(unitPrice*float64(qty), discountPct)\n}\n"
const lineV2 = "package pricing\n\nfunc LineTotal(unitPrice float64, qty int, discountPct float64) float64 {\n\treturn Discount(unitPrice*float64(qty), discountPct*100)\n}\n"
const moneyNeg = "package format\n\nimport \"fmt\"\n\nfunc FormatUSD(amount float64) string {\n\tif amount < 0 {\n\t\treturn \"(\" + FormatUSD(-amount) + \")\"\n\t}\n\treturn fmt.Sprintf(\"$%.2f\", amount)\n}\n"

func TestGitOracleIsSemantic(t *testing.T) {
	a, _, _ := newGit(t)
	c0Files := GitC0()
	s0 := map[string]string{}
	for p, c := range c0Files {
		s0[p] = c
	}
	s0["pricing/line.go"], s0["format/money.go"] = lineV1, moneyNeg
	if ok, out := OracleGit(s0, ""); !ok {
		t.Fatalf("correct S0 solution fails S0 oracle: %s", out)
	}
	c1, _ := a.ConcurrentCommit(GitMutations["G1"], "G1")
	c1Files, _ := a.Files(c1)
	stale, conf := Rebase(c0Files, c1Files, s0)
	if len(conf) != 0 {
		t.Fatal(conf)
	}
	if ok, _ := OracleGit(stale, "G1"); ok {
		t.Fatal("stale S0 solution passes the S1 oracle: mutation is not semantic")
	}
	stale["pricing/line.go"] = lineV2
	if ok, out := OracleGit(stale, "G1"); !ok {
		t.Fatalf("correct S1 solution fails: %s", out)
	}
}

func TestHTTPOracle(t *testing.T) {
	dir := t.TempDir()
	WriteFiles(dir, map[string]string{
		"quote.json":  `{"customer":"c42","sku":"widget","qty":3,"unit_price":40,"discount_pct":10,"total":108}`,
		"customer.md": "Acme Ltd is a gold customer with a 10% discount.",
	})
	e0, _ := ExpectFrom(HTTPS0())
	if ok, out := OracleHTTP(dir, e0); !ok {
		t.Fatal(out)
	}
	s1 := HTTPS0()
	for p, b := range HTTPMutations["H1"] {
		s1[p] = b
	}
	e1, _ := ExpectFrom(s1)
	if ok, _ := OracleHTTP(dir, e1); ok {
		t.Fatal("S0 quote passes S1 oracle")
	}
}

func TestEvaluateExcludesFreshFirstCalls(t *testing.T) {
	mk := func(newWork ...int64) []*Call {
		var cs []*Call
		for _, n := range newWork {
			cs = append(cs, &Call{Usage: true, Tokens: Tokens{Input: n}})
		}
		return cs
	}
	s0, r0, c := mk(8000, 100, 100, 100), mk(8000, 100, 100, 100), mk(100)
	sc := &Scenario{Qualifying: true, Preserved: 3, PostFirst: 2, R2Oracle: true, R0Oracle: true, R2Committed: true}
	sc.R1.addSegment(s0, true, 0)
	sc.R1.addSegment(r0, true, 0)
	sc.R2.addSegment(s0, true, 0)
	sc.R2.addSegment(c, false, 0)
	g := Evaluate(sc, s0, r0, c)
	// excl first: R1 = 300+300, R2 = 300+100 -> 1/3 reduction.
	if g.PrimaryReduction < 0.333 || g.PrimaryReduction > 0.334 || !g.PrimaryPass || !g.Win {
		t.Fatalf("gates %+v", g)
	}
	// The prefill dominates the including-first variant.
	if g.InclFirstReduction > 0.5 || g.InclFirstReduction < 0.45 {
		t.Fatalf("incl-first %v", g.InclFirstReduction)
	}
	sc.PostFirst = 0
	if Evaluate(sc, s0, r0, c).Win {
		t.Fatal("a win requires a preserved post-first call")
	}
}

func TestDecide(t *testing.T) {
	win := Gates{Win: true, Correct: true, UsageComplete: true, PrimaryReduction: 0.5, PreservedPostFirst: true}
	weak := Gates{Correct: true, UsageComplete: true, PrimaryReduction: 0.1, PreservedPostFirst: true}
	none := Gates{Correct: true, UsageComplete: true}
	g := Scenario{ID: "G1", Family: "git", Qualifying: true}
	h := Scenario{ID: "H1", Family: "http", Qualifying: true}
	if d, _ := Decide([]Scenario{g, h}, []Gates{win, win}, true); d != DecisionContinue {
		t.Fatal(d)
	}
	if d, _ := Decide([]Scenario{g, h}, []Gates{win, weak}, true); d != DecisionGuardOnly {
		t.Fatal(d)
	}
	if d, _ := Decide([]Scenario{g, h}, []Gates{none, none}, true); d != DecisionKill {
		t.Fatal(d)
	}
	if d, _ := Decide([]Scenario{g, h}, []Gates{win, {Win: true, Correct: true}}, true); d != DecisionTelemetry {
		t.Fatal(d)
	}
	g2 := g
	g2.MissedStale = 1
	if d, _ := Decide([]Scenario{g2, h}, []Gates{win, win}, true); d != DecisionKill {
		t.Fatal(d)
	}
	// Every repetition must win.
	if d, _ := Decide([]Scenario{g, g, h}, []Gates{win, weak, win}, true); d == DecisionContinue {
		t.Fatal("a failing repetition must block the win")
	}
}

func TestParseRealOpenCodeStream(t *testing.T) {
	p := filepath.Join("..", "..", "..", "docs", "research", "live-agent-premise", "runs", "ho-local2-conflict", "stream.jsonl")
	s, err := ParseStream(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Calls) != 11 || s.SessionID == "" {
		t.Fatalf("calls=%d session=%q", len(s.Calls), s.SessionID)
	}
	c0 := s.Calls[0].Tokens
	if c0.Input != 7875 || c0.Output != 82 || c0.Reasoning != 81 || c0.CacheRead != 1521 || !s.Calls[0].Usage || s.Calls[0].Snapshot == "" {
		t.Fatalf("first call %+v", s.Calls[0])
	}
	for i, c := range s.Calls {
		if c.Steps != 1 || !c.Usage {
			t.Fatalf("call %d steps=%d usage=%v", i, c.Steps, c.Usage)
		}
	}
	if s.Calls[0].Tools[0].Tool != "read" {
		t.Fatalf("tool %+v", s.Calls[0].Tools)
	}
}

func TestUsageFromExportFillsFinalCall(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "docs", "research", "real-replay-economics", "runs", "smoke-1")
	s, err := ParseStream(filepath.Join(dir, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	last := s.Calls[len(s.Calls)-1]
	if last.Usage {
		t.Fatal("expected the recorded stream to omit the final step_finish")
	}
	if err := fillUsageFromExport(s, filepath.Join(dir, "export.json")); err != nil {
		t.Fatal(err)
	}
	for i, c := range s.Calls {
		if !c.Usage {
			t.Fatalf("call %d still lacks usage", i)
		}
	}
	if last.Tokens.Input != 821 || last.Tokens.Output != 44 || last.Tokens.CacheRead != 9841 {
		t.Fatalf("final call usage %+v", last.Tokens)
	}
}
