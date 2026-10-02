package realreplay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SmokeReport summarizes the two unscored setup invocations.
type SmokeReport struct {
	S0Calls      int               `json:"s0_calls"`
	S0Tools      map[string]int    `json:"s0_tools"`
	Premises     []Premise         `json:"s0_premises"`
	FirstStale   int               `json:"first_stale_call_under_H1"`
	Continuation ContinuationStats `json:"continuation"`
	ResumeCalls  int               `json:"resume_calls"`
	ResumeTokens []Tokens          `json:"resume_call_tokens"`
	S0Tokens     []Tokens          `json:"s0_call_tokens"`
	Notes        []string          `json:"notes"`
}

// Smoke runs the unscored setup check: one HTTP S0 execution (smoke-1) and
// one real resume of it under H1 (smoke-2). It validates capture, ETag
// binding, continuation import and resume mechanics before the freeze.
func (r *Runner) Smoke(ctx context.Context) (*SmokeReport, error) {
	rep := &SmokeReport{S0Tools: map[string]int{}}
	base := filepath.Join(r.Work, "smoke")
	if err := ClearDir(base); err != nil {
		return nil, err
	}
	ws := filepath.Join(r.Work, "ws-smoke")
	aw, aw0, err := NewAuthority(filepath.Join(base, "authw"), HTTPWorkspace())
	if err != nil {
		return nil, err
	}
	srv, err := NewHTTPAuthority(HTTPAddr, HTTPS0())
	if err != nil {
		return nil, err
	}
	defer srv.Close()
	if err := aw.Materialize(ws, aw0); err != nil {
		return nil, err
	}
	start := httpEtags(srv)
	s0, err := r.invoke(ctx, "smoke-1", ws, TaskHTTP, "")
	if err != nil {
		return rep, err
	}
	if err := checkAlignment(s0, 0); err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	if err := checkSnapshot0(s0, HTTPWorkspace()); err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	rep.S0Calls = len(s0.Stream.Calls)
	for _, c := range s0.Stream.Calls {
		rep.S0Tokens = append(rep.S0Tokens, c.Tokens)
		for _, t := range c.Tools {
			rep.S0Tools[t.Tool]++
		}
	}
	w := &World{Workspace: ws, Git: aw, Base: aw0, HTTP: srv, HTTPLog: srv.Log(), HTTPStart: start}
	ps, err := w.Premises(s0.Stream.Calls, 0)
	if err != nil {
		return rep, err
	}
	rep.Premises = ps
	for p, b := range HTTPMutations["H1"] {
		srv.Set(p, b)
	}
	v, err := Validate(ps, aw, srv)
	if err != nil {
		return rep, err
	}
	rep.FirstStale = v.FirstStale
	if v.FirstStale <= 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("first stale call %d: no preserved prefix to resume", v.FirstStale))
		return rep, nil
	}
	snap, err := SnapshotFiles(s0.Stream.Calls[v.FirstStale].Snapshot)
	if err != nil {
		return rep, err
	}
	if err := setWorkspace(aw, ws, aw0, snap); err != nil {
		return rep, err
	}
	c, st, err := r.resume(ctx, "smoke-2", ws, s0.Export, v.FirstStale, "Sm01")
	rep.Continuation = st
	if c != nil {
		rep.ResumeCalls = len(c.Stream.Calls)
		for _, k := range c.Stream.Calls {
			rep.ResumeTokens = append(rep.ResumeTokens, k.Tokens)
		}
	}
	if err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	return rep, nil
}

// SmokeResume re-attempts smoke-2 from the recorded smoke-1 run (its
// export, parsed calls and premises), after the pre-freeze path fix. The
// fixtures are deterministic, so the authorities are rebuilt identically.
func (r *Runner) SmokeResume(ctx context.Context) (*SmokeReport, error) {
	rep := &SmokeReport{S0Tools: map[string]int{}}
	dir := filepath.Join(r.Out, "runs", "smoke-1")
	st, err := ParseStream(filepath.Join(dir, "stream.jsonl"))
	if err != nil {
		return nil, err
	}
	export := filepath.Join(dir, "export.json")
	if err := fillUsageFromExport(st, export); err != nil {
		return nil, err
	}
	base := filepath.Join(r.Work, "smoke2")
	if err := ClearDir(base); err != nil {
		return nil, err
	}
	ws := filepath.Join(r.Work, "ws-smoke")
	aw, aw0, err := NewAuthority(filepath.Join(base, "authw"), HTTPWorkspace())
	if err != nil {
		return nil, err
	}
	srv, err := NewHTTPAuthority(HTTPAddr, HTTPS0())
	if err != nil {
		return nil, err
	}
	defer srv.Close()
	for p, b := range HTTPMutations["H1"] {
		srv.Set(p, b)
	}
	// First stale call from the smoke-1 premises recorded in report.json:
	// the /pricing/widget fetch. Recomputed here from tool inputs.
	j := -1
	for i, c := range st.Calls {
		for _, t := range c.Tools {
			if t.Tool == "webfetch" && strings.HasSuffix(str(t.Input, "url"), "/pricing/widget") && j < 0 {
				j = i
			}
		}
	}
	rep.S0Calls, rep.FirstStale = len(st.Calls), j
	for _, c := range st.Calls {
		rep.S0Tokens = append(rep.S0Tokens, c.Tokens)
	}
	if j <= 0 {
		return rep, fmt.Errorf("no preserved prefix")
	}
	snap, err := SnapshotFiles(st.Calls[j].Snapshot)
	if err != nil {
		return rep, err
	}
	if err := setWorkspace(aw, ws, aw0, snap); err != nil {
		return rep, err
	}
	c, cs, err := r.resume(ctx, "smoke-2", ws, export, j, "Sm02")
	rep.Continuation = cs
	if c != nil {
		rep.ResumeCalls = len(c.Stream.Calls)
		for _, k := range c.Stream.Calls {
			rep.ResumeTokens = append(rep.ResumeTokens, k.Tokens)
			for _, t := range k.Tools {
				rep.S0Tools["resume:"+t.Tool]++
			}
		}
		served := srv.Log()
		_ = writeJSON(filepath.Join(c.Dir, "http-served.json"), served)
		if q, err := os.ReadFile(filepath.Join(ws, "quote.json")); err == nil {
			exp, _ := ExpectFrom(srv.State())
			ok, out := OracleHTTP(ws, exp)
			rep.Notes = append(rep.Notes, fmt.Sprintf("resume quote.json=%s oracle(S1) pass=%v %s", strings.TrimSpace(string(q)), ok, out))
		}
	}
	if err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	return rep, nil
}
