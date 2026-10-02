package realreplay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// checkSnapshot0 verifies that OpenCode's snapshot at the first call is
// exactly the materialized base (the reconstruction source is faithful).
func checkSnapshot0(rec *RunRec, base map[string]string) error {
	if len(rec.Stream.Calls) == 0 || rec.Stream.Calls[0].Snapshot == "" {
		return fmt.Errorf("%s: no snapshot on first call", rec.Label)
	}
	snap, err := SnapshotFiles(rec.Stream.Calls[0].Snapshot)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(snap, base) {
		return fmt.Errorf("%s: first-call snapshot differs from base (%d vs %d files)", rec.Label, len(snap), len(base))
	}
	return nil
}

// GitFamily runs one S0 execution and, for each mutation, R2 (actual
// selective replay) and R0 (fresh execution on the final state, which is
// also R1's restart).
func (r *Runner) GitFamily(ctx context.Context, rep string, muts []string) ([]Outcome, error) {
	base := filepath.Join(r.Work, "git"+rep)
	if err := ClearDir(base); err != nil {
		return nil, err
	}
	ws := filepath.Join(r.Work, "ws-git")
	a0, c0, err := NewAuthority(filepath.Join(base, "auth0"), GitC0())
	if err != nil {
		return nil, err
	}
	if err := a0.Materialize(ws, c0); err != nil {
		return nil, err
	}
	s0, err := r.invoke(ctx, "git"+rep+"-S0", ws, TaskGit, "")
	if err != nil {
		return nil, err
	}
	if err := checkAlignment(s0, 0); err != nil {
		return nil, err
	}
	if err := checkSnapshot0(s0, GitC0()); err != nil {
		return nil, err
	}
	s0Files, err := ReadWorkspace(ws)
	if err != nil {
		return nil, err
	}
	w0 := &World{Workspace: ws, Git: a0, Base: c0}
	s0Prem, err := w0.Premises(s0.Stream.Calls, 0)
	if err != nil {
		return nil, err
	}
	_ = writeJSON(filepath.Join(s0.Dir, "premises.json"), s0Prem)
	if ok, out := OracleGit(s0Files, ""); true {
		_ = os.WriteFile(filepath.Join(s0.Dir, "oracle-on-S0.txt"), []byte(fmt.Sprintf("pass=%v\n%s", ok, out)), 0o644)
	}

	if err := os.MkdirAll(filepath.Join(r.Out, "results"), 0o755); err != nil {
		return nil, err
	}
	var outs []Outcome
	for _, m := range muts {
		o := Outcome{OracleOut: map[string]string{}}
		sc := &o.Scenario
		sc.ID, sc.Family, sc.Mutation, sc.Qualifying = m+rep, "git", m, m == "G1"
		sc.S0Calls = len(s0.Stream.Calls)
		am, err := a0.Clone(filepath.Join(base, "auth-"+m))
		if err != nil {
			return outs, err
		}
		c1, err := am.ConcurrentCommit(GitMutations[m], "concurrent "+m)
		if err != nil {
			return outs, err
		}
		c1Files, _ := am.Files(c1)
		v, err := Validate(s0Prem, am, nil)
		if err != nil {
			return outs, err
		}
		o.S0Valid = v
		j := v.FirstStale
		sc.FirstStale = j
		// The S0 result itself, rebased onto S1, must be wrong on S1 for a
		// semantic mutation (proves the change matters).
		reb, conf := Rebase(GitC0(), c1Files, s0Files)
		staleOK, staleOut := false, "conflict: "+fmt.Sprint(conf)
		if len(conf) == 0 {
			staleOK, staleOut = OracleGit(reb, m)
		}
		sc.StaleS0OnS1Fail = !staleOK
		o.OracleOut["stale_s0_on_s1"] = staleOut

		// R0 runs on a separate authority clone at the identical S1 commit.
		runR0 := func() (*RunRec, error) {
			ar0, err := a0.Clone(filepath.Join(base, "auth-"+m+"-r0"))
			if err != nil {
				return nil, err
			}
			c1b, err := ar0.ConcurrentCommit(GitMutations[m], "concurrent "+m)
			if err != nil {
				return nil, err
			}
			if c1b != c1 {
				return nil, fmt.Errorf("non-deterministic S1: %s vs %s", c1b, c1)
			}
			if err := ar0.Materialize(ws, c1); err != nil {
				return nil, err
			}
			rec, err := r.invoke(ctx, "git"+rep+"-"+m+"-R0", ws, TaskGit, "")
			if err != nil {
				return rec, err
			}
			if err := checkAlignment(rec, 0); err != nil {
				return rec, err
			}
			wr := &World{Workspace: ws, Git: ar0, Base: c1}
			pr, err := wr.Premises(rec.Stream.Calls, 0)
			if err != nil {
				return rec, err
			}
			fv, err := Validate(pr, ar0, nil)
			if err != nil {
				return rec, err
			}
			o.R0Final = &fv
			_ = writeJSON(filepath.Join(rec.Dir, "premises.json"), pr)
			if len(fv.Stale) == 0 && len(fv.Unknown) == 0 {
				if head, err := ar0.CommitWorkspace(ws, c1, "R0 "+m); err == nil {
					sc.R0Committed = true
					files, _ := ar0.Files(head)
					sc.R0Oracle, o.OracleOut["r0"] = OracleGit(files, m)
				} else {
					o.OracleOut["r0"] = err.Error()
				}
			} else {
				o.OracleOut["r0"] = "R0 premises not fresh at commit; not committed"
			}
			return rec, nil
		}

		var r0 *RunRec
		var cCalls []*Call
		sc.R1.addSegment(s0.Stream.Calls, true, s0.Inv.WallMS)
		sc.R2.addSegment(s0.Stream.Calls, true, s0.Inv.WallMS)
		var accepted []Premise

		switch {
		case j < 0:
			sc.R2Mode = "commit-only"
			sc.Preserved = len(s0.Stream.Calls)
			if err := setWorkspace(am, ws, c1, reb); err != nil {
				return outs, err
			}
			accepted = s0Prem
		case j == 0:
			sc.R2Mode = "restart-equivalent"
		default:
			snap, err := SnapshotFiles(s0.Stream.Calls[j].Snapshot)
			if err != nil {
				return outs, err
			}
			rb, conf := Rebase(GitC0(), c1Files, snap)
			if len(conf) > 0 {
				sc.R2Mode = "fallback-restart"
				sc.Notes = append(sc.Notes, fmt.Sprintf("rebase conflict %v: fell back to restart", conf))
				break
			}
			sc.R2Mode = "resume"
			sc.Preserved = j
			if err := setWorkspace(am, ws, c1, rb); err != nil {
				return outs, err
			}
			c, st, err := r.resume(ctx, "git"+rep+"-"+m+"-R2", ws, s0.Export, j, contTag(m, rep, 1))
			o.Continuation = append(o.Continuation, st)
			if err != nil {
				return outs, err
			}
			cCalls = c.Stream.Calls
			sc.R2.addSegment(cCalls, false, c.Inv.WallMS)
			wc := &World{Workspace: ws, Git: am, Base: c1}
			cp, err := wc.Premises(cCalls, j)
			if err != nil {
				return outs, err
			}
			for _, p := range s0Prem {
				if p.Call < j {
					accepted = append(accepted, p)
				}
			}
			accepted = append(accepted, cp...)
			_ = writeJSON(filepath.Join(c.Dir, "premises.json"), cp)
		}
		if sc.R2Mode == "commit-only" || sc.R2Mode == "resume" {
			sc.R2Rounds = 1
			if sc.R2Mode == "commit-only" {
				sc.R2Rounds = 0
			}
			fv, err := Validate(accepted, am, nil)
			if err != nil {
				return outs, err
			}
			o.R2Final = &fv
			sc.UnknownPremises = countUnknown(accepted)
			if len(fv.Stale) == 0 && len(fv.Unknown) == 0 {
				if head, err := am.CommitWorkspace(ws, c1, "R2 "+m); err == nil {
					sc.R2Committed = true
					files, _ := am.Files(head)
					sc.R2Oracle, o.OracleOut["r2"] = OracleGit(files, m)
				} else {
					o.OracleOut["r2"] = err.Error()
				}
			} else {
				o.OracleOut["r2"] = "R2 premises not fresh at commit; not committed"
			}
		}
		if sc.Preserved > 0 {
			sc.PostFirst = sc.Preserved - 1
		}

		r0, err = runR0()
		if err != nil {
			return outs, err
		}
		sc.R1.addSegment(r0.Stream.Calls, true, r0.Inv.WallMS)
		sc.R1Oracle = sc.R0Oracle
		if sc.R2Mode == "restart-equivalent" || sc.R2Mode == "fallback-restart" {
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
		if o.R2Final != nil && sc.R2Committed {
			sc.MissedStale = len(o.R2Final.Stale)
		}
		r0calls := r0.Stream.Calls
		if sc.R2Mode == "restart-equivalent" || sc.R2Mode == "fallback-restart" {
			o.Gates = Evaluate(sc, s0.Stream.Calls, r0calls, r0calls)
		} else {
			o.Gates = Evaluate(sc, s0.Stream.Calls, r0calls, cCalls)
		}
		_ = writeJSON(filepath.Join(r.Out, "results", sc.ID+".json"), o)
		outs = append(outs, o)
	}
	return outs, nil
}

// contTag is the 4-char message-ID suffix for a continuation: mutation id,
// repetition, round.
func contTag(m, rep string, round int) string {
	r := "1"
	if rep != "" {
		r = "2"
	}
	return fmt.Sprintf("%s%s%d", m, r, round)
}
