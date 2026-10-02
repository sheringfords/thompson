package realreplay

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// httpCommit validates accepted premises and, if all are fresh, commits
// quote.json to /quotes/q1 with If-Match on the target plus every HTTP
// premise as a precondition. Returns (committed, oracle pass, detail).
func httpCommit(ws string, srv *HTTPAuthority, aw *Authority, accepted []Premise) (Validation, bool, bool, string, error) {
	fv, err := Validate(accepted, aw, srv)
	if err != nil {
		return fv, false, false, "", err
	}
	if len(fv.Stale) > 0 || len(fv.Unknown) > 0 {
		return fv, false, false, "premises not fresh at commit; not committed", nil
	}
	pre := map[string]string{}
	for _, p := range accepted {
		if p.Kind != KindHTTP {
			continue
		}
		if w, ok := pre[p.Key]; ok && w != p.Witness {
			return fv, false, false, "conflicting witnesses for " + p.Key, nil
		}
		pre[p.Key] = p.Witness
	}
	quote, err := os.ReadFile(filepath.Join(ws, "quote.json"))
	if err != nil {
		return fv, false, false, "quote.json missing; not committed", nil
	}
	_, tag, _ := srv.Current("/quotes/q1")
	code, err := ConditionalPut(srv.Addr, "/quotes/q1", tag, string(quote), pre)
	if err != nil {
		return fv, false, false, "", err
	}
	if code != http.StatusOK {
		return fv, false, false, fmt.Sprintf("conditional commit rejected: %d", code), nil
	}
	state := srv.State()
	exp, err := ExpectFrom(state)
	if err != nil {
		return fv, true, false, err.Error(), nil
	}
	ok, out := OracleHTTP(ws, exp)
	if state["/quotes/q1"] != string(quote) {
		ok, out = false, out+"; committed q1 differs from quote.json"
	}
	return fv, true, ok, out, nil
}

// HTTPFamily runs one S0 execution against the HTTP fixture and, for each
// mutation, R2 and R0. If withH1x, it also runs the H1x adversarial case: a
// second change lands during the H1 replay and must trigger another
// validation/replay round instead of a silent commit.
func (r *Runner) HTTPFamily(ctx context.Context, rep string, muts []string, withH1x bool) ([]Outcome, *Outcome, error) {
	base := filepath.Join(r.Work, "http"+rep)
	if err := ClearDir(base); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Join(r.Out, "results"), 0o755); err != nil {
		return nil, nil, err
	}
	ws := filepath.Join(r.Work, "ws-http")
	aw, aw0, err := NewAuthority(filepath.Join(base, "authw"), HTTPWorkspace())
	if err != nil {
		return nil, nil, err
	}
	srv, err := NewHTTPAuthority(HTTPAddr, HTTPS0())
	if err != nil {
		return nil, nil, err
	}
	defer srv.Close()

	srv.Reset(HTTPS0())
	start := httpEtags(srv)
	if err := aw.Materialize(ws, aw0); err != nil {
		return nil, nil, err
	}
	s0, err := r.invoke(ctx, "http"+rep+"-S0", ws, TaskHTTP, "")
	if err != nil {
		return nil, nil, err
	}
	if err := checkAlignment(s0, 0); err != nil {
		return nil, nil, err
	}
	if err := checkSnapshot0(s0, HTTPWorkspace()); err != nil {
		return nil, nil, err
	}
	s0Log := srv.Log()
	_ = writeJSON(filepath.Join(s0.Dir, "http-served.json"), s0Log)
	s0Files, err := ReadWorkspace(ws)
	if err != nil {
		return nil, nil, err
	}
	w0 := &World{Workspace: ws, Git: aw, Base: aw0, HTTP: srv, HTTPLog: s0Log, HTTPStart: start}
	s0Prem, err := w0.Premises(s0.Stream.Calls, 0)
	if err != nil {
		return nil, nil, err
	}
	_ = writeJSON(filepath.Join(s0.Dir, "premises.json"), s0Prem)

	resetTo := func(m string) {
		srv.SetAfterServe(nil)
		srv.Reset(HTTPS0())
		for p, b := range HTTPMutations[m] {
			srv.Set(p, b)
		}
	}
	// staleCheck: S0's own outputs judged against the final state.
	staleCheck := func() (bool, string) {
		dir, _ := os.MkdirTemp("", "rre-stale-*")
		defer os.RemoveAll(dir)
		_ = WriteFiles(dir, s0Files)
		exp, _ := ExpectFrom(srv.State())
		return OracleHTTP(dir, exp)
	}
	// resumeRound runs one selective-replay round from export, keeping
	// keep assistant messages; snapshotTree is the workspace snapshot
	// before the first stale call.
	resumeRound := func(label, export, snapshotTree string, keep int, tag string, o *Outcome) (*RunRec, []Premise, error) {
		snap, err := SnapshotFiles(snapshotTree)
		if err != nil {
			return nil, nil, err
		}
		rb, conf := Rebase(HTTPWorkspace(), HTTPWorkspace(), snap)
		if len(conf) > 0 {
			return nil, nil, fmt.Errorf("unexpected workspace conflict %v", conf)
		}
		if err := setWorkspace(aw, ws, aw0, rb); err != nil {
			return nil, nil, err
		}
		startC := httpEtags(srv)
		n0 := len(srv.Log())
		c, st, err := r.resume(ctx, label, ws, export, keep, tag)
		o.Continuation = append(o.Continuation, st)
		if err != nil {
			return c, nil, err
		}
		logC := srv.Log()[n0:]
		_ = writeJSON(filepath.Join(c.Dir, "http-served.json"), logC)
		wc := &World{Workspace: ws, Git: aw, Base: aw0, HTTP: srv, HTTPLog: logC, HTTPStart: startC}
		cp, err := wc.Premises(c.Stream.Calls, keep)
		if err != nil {
			return c, nil, err
		}
		_ = writeJSON(filepath.Join(c.Dir, "premises.json"), cp)
		return c, cp, nil
	}
	prefix := func(ps []Premise, j int) []Premise {
		var out []Premise
		for _, p := range ps {
			if p.Call < j {
				out = append(out, p)
			}
		}
		return out
	}

	var outs []Outcome
	for _, m := range muts {
		o := Outcome{OracleOut: map[string]string{}}
		sc := &o.Scenario
		sc.ID, sc.Family, sc.Mutation, sc.Qualifying = m+rep, "http", m, m == "H1"
		sc.S0Calls = len(s0.Stream.Calls)
		resetTo(m)
		v, err := Validate(s0Prem, aw, srv)
		if err != nil {
			return outs, nil, err
		}
		o.S0Valid = v
		j := v.FirstStale
		sc.FirstStale = j
		ok, out := staleCheck()
		sc.StaleS0OnS1Fail = !ok
		o.OracleOut["stale_s0_on_s1"] = out

		sc.R1.addSegment(s0.Stream.Calls, true, s0.Inv.WallMS)
		sc.R2.addSegment(s0.Stream.Calls, true, s0.Inv.WallMS)
		var cCalls []*Call
		var accepted []Premise
		switch {
		case j < 0:
			sc.R2Mode, sc.Preserved = "commit-only", len(s0.Stream.Calls)
			if err := setWorkspace(aw, ws, aw0, s0Files); err != nil {
				return outs, nil, err
			}
			accepted = s0Prem
		case j == 0:
			sc.R2Mode = "restart-equivalent"
		default:
			sc.R2Mode, sc.Preserved, sc.R2Rounds = "resume", j, 1
			c, cp, err := resumeRound("http"+rep+"-"+m+"-R2", s0.Export, s0.Stream.Calls[j].Snapshot, j, contTag(m, rep, 1), &o)
			if err != nil {
				return outs, nil, err
			}
			cCalls = c.Stream.Calls
			sc.R2.addSegment(cCalls, false, c.Inv.WallMS)
			accepted = append(prefix(s0Prem, j), cp...)
		}
		if sc.R2Mode == "commit-only" || sc.R2Mode == "resume" {
			fv, committed, pass, detail, err := httpCommit(ws, srv, aw, accepted)
			if err != nil {
				return outs, nil, err
			}
			o.R2Final = &fv
			sc.R2Committed, sc.R2Oracle, o.OracleOut["r2"] = committed, pass, detail
			sc.UnknownPremises = countUnknown(accepted)
			if committed {
				sc.MissedStale = len(fv.Stale)
			}
		}
		if sc.Preserved > 0 {
			sc.PostFirst = sc.Preserved - 1
		}

		// R0: fresh execution on the final state.
		resetTo(m)
		if err := aw.Materialize(ws, aw0); err != nil {
			return outs, nil, err
		}
		startR0 := httpEtags(srv)
		n0 := len(srv.Log())
		r0, err := r.invoke(ctx, "http"+rep+"-"+m+"-R0", ws, TaskHTTP, "")
		if err != nil {
			return outs, nil, err
		}
		if err := checkAlignment(r0, 0); err != nil {
			return outs, nil, err
		}
		logR0 := srv.Log()[n0:]
		_ = writeJSON(filepath.Join(r0.Dir, "http-served.json"), logR0)
		wr := &World{Workspace: ws, Git: aw, Base: aw0, HTTP: srv, HTTPLog: logR0, HTTPStart: startR0}
		pr, err := wr.Premises(r0.Stream.Calls, 0)
		if err != nil {
			return outs, nil, err
		}
		_ = writeJSON(filepath.Join(r0.Dir, "premises.json"), pr)
		fv0, committed0, pass0, detail0, err := httpCommit(ws, srv, aw, pr)
		if err != nil {
			return outs, nil, err
		}
		o.R0Final = &fv0
		sc.R0Committed, sc.R0Oracle, o.OracleOut["r0"] = committed0, pass0, detail0
		sc.R1Oracle = sc.R0Oracle
		sc.R1.addSegment(r0.Stream.Calls, true, r0.Inv.WallMS)
		if sc.R2Mode == "restart-equivalent" {
			sc.R2.addSegment(r0.Stream.Calls, true, r0.Inv.WallMS)
			cCalls = r0.Stream.Calls
			sc.R2Oracle, sc.R2Committed = sc.R0Oracle, sc.R0Committed
			o.R2Final = o.R0Final
			o.OracleOut["r2"] = "same execution as R0 (restart-equivalent)"
		}
		from := j
		if from < 0 {
			from = len(s0.Stream.Calls)
		}
		sc.Discard.R2True, sc.Discard.R2Opaque, _ = classifyDiscards(s0Prem, v, from, len(s0.Stream.Calls))
		sc.Discard.R1True, sc.Discard.R1Opaque, sc.Discard.R1False = classifyDiscards(s0Prem, v, 0, len(s0.Stream.Calls))
		o.Gates = Evaluate(sc, s0.Stream.Calls, r0.Stream.Calls, cCalls)
		_ = writeJSON(filepath.Join(r.Out, "results", sc.ID+".json"), o)
		outs = append(outs, o)
	}
	if !withH1x {
		return outs, nil, nil
	}

	// H1x: second change during replay.
	o := Outcome{OracleOut: map[string]string{}}
	sc := &o.Scenario
	sc.ID, sc.Family, sc.Mutation = "H1x"+rep, "http", "H1+second"
	sc.S0Calls = len(s0.Stream.Calls)
	resetTo("H1")
	v, err := Validate(s0Prem, aw, srv)
	if err != nil {
		return outs, nil, err
	}
	o.S0Valid = v
	j := v.FirstStale
	sc.FirstStale = j
	if j <= 0 {
		sc.Notes = append(sc.Notes, "H1x not applicable: no preserved prefix")
		_ = writeJSON(filepath.Join(r.Out, "results", sc.ID+".json"), o)
		return outs, &o, nil
	}
	fired := false
	srv.SetAfterServe(func(path string) map[string]string {
		if path == "/pricing/widget" && !fired {
			fired = true
			return H1xSecond
		}
		return nil
	})
	sc.R2.addSegment(s0.Stream.Calls, true, s0.Inv.WallMS)
	sc.Preserved = j
	c1, cp1, err := resumeRound("http"+rep+"-H1x-R2a", s0.Export, s0.Stream.Calls[j].Snapshot, j, contTag("Hx", rep, 1), &o)
	srv.SetAfterServe(nil)
	if err != nil {
		return outs, nil, err
	}
	sc.R2.addSegment(c1.Stream.Calls, false, c1.Inv.WallMS)
	acc1 := append(prefix(s0Prem, j), cp1...)
	v1, err := Validate(acc1, aw, srv)
	if err != nil {
		return outs, nil, err
	}
	o.Rounds = append(o.Rounds, v1)
	sc.R2Rounds = 1
	if !fired {
		sc.Notes = append(sc.Notes, "second change never fired (pricing not re-fetched during replay)")
	}
	if v1.FirstStale < 0 {
		sc.Notes = append(sc.Notes, "round-1 validation found nothing stale")
		_ = writeJSON(filepath.Join(r.Out, "results", sc.ID+".json"), o)
		return outs, &o, nil
	}
	g := v1.FirstStale
	var acc []Premise
	if g < j {
		sc.Notes = append(sc.Notes, "second change invalidated preserved S0 prefix")
		_ = writeJSON(filepath.Join(r.Out, "results", sc.ID+".json"), o)
		return outs, &o, nil
	}
	local := g - j
	c2, cp2, err := resumeRound("http"+rep+"-H1x-R2b", c1.Export, c1.Stream.Calls[local].Snapshot, g, contTag("Hx", rep, 2), &o)
	if err != nil {
		return outs, nil, err
	}
	sc.R2.addSegment(c2.Stream.Calls, false, c2.Inv.WallMS)
	sc.R2Rounds = 2
	acc = append(prefix(acc1, g), cp2...)
	fv, committed, pass, detail, err := httpCommit(ws, srv, aw, acc)
	if err != nil {
		return outs, nil, err
	}
	o.R2Final = &fv
	sc.R2Committed, sc.R2Oracle, o.OracleOut["r2"] = committed, pass, detail
	if committed {
		sc.MissedStale = len(fv.Stale)
	}
	_ = writeJSON(filepath.Join(r.Out, "results", sc.ID+".json"), o)
	return outs, &o, nil
}
