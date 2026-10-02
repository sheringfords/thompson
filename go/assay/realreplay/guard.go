package realreplay

import (
	"net/http"
	"path/filepath"
)

// RaceProbe is one "state changes after validation, before commit" probe
// against a real authority. Pass means the authority rejected the commit.
type RaceProbe struct {
	Name     string `json:"name"`
	Rejected bool   `json:"rejected"`
	Detail   string `json:"detail"`
}

// CommitRaceProbes exercises the conditional-commit path of both
// authorities with a change landing between validation and commit. No
// model is involved.
func CommitRaceProbes(work string, httpAddr string) ([]RaceProbe, error) {
	var out []RaceProbe
	if err := ClearDir(work); err != nil {
		return nil, err
	}
	// Git: validate at C0, a concurrent commit lands, CAS commit from C0.
	a, c0, err := NewAuthority(filepath.Join(work, "auth"), GitC0())
	if err != nil {
		return nil, err
	}
	ws := filepath.Join(work, "ws")
	if err := a.Materialize(ws, c0); err != nil {
		return nil, err
	}
	if err := WriteFiles(ws, map[string]string{"pricing/line.go": "package pricing\n"}); err != nil {
		return nil, err
	}
	w := &World{Workspace: ws, Git: a, Base: c0}
	ps, _ := w.Premises([]*Call{{Tools: []ToolUse{{Tool: "read", Input: map[string]interface{}{"path": filepath.Join(ws, "pricing/discount.go")}}}}}, 0)
	v, err := Validate(ps, a, nil)
	if err != nil {
		return nil, err
	}
	if len(v.Stale) != 0 {
		out = append(out, RaceProbe{"git-cas", false, "premise unexpectedly stale before race"})
	} else {
		if _, err := a.ConcurrentCommit(GitMutations["G1"], "race"); err != nil {
			return nil, err
		}
		_, err := a.CommitWorkspace(ws, c0, "racing commit")
		out = append(out, RaceProbe{"git-cas", err != nil, errString(err)})
	}

	// HTTP: validate premises, a premise resource changes, conditional PUT.
	srv, err := NewHTTPAuthority(httpAddr, HTTPS0())
	if err != nil {
		return nil, err
	}
	defer srv.Close()
	_, priceTag, _ := srv.Current("/pricing/widget")
	_, qTag, _ := srv.Current("/quotes/q1")
	srv.Set("/pricing/widget", HTTPMutations["H1"]["/pricing/widget"])
	code, err := ConditionalPut(srv.Addr, "/quotes/q1", qTag, `{"total":108}`, map[string]string{"/pricing/widget": priceTag})
	if err != nil {
		return nil, err
	}
	out = append(out, RaceProbe{"http-premise-precondition", code == http.StatusPreconditionFailed, http.StatusText(code)})
	// HTTP: the target itself changes after validation (If-Match).
	srv.Reset(HTTPS0())
	_, qTag, _ = srv.Current("/quotes/q1")
	srv.Set("/quotes/q1", `{"status":"taken"}`)
	code, err = ConditionalPut(srv.Addr, "/quotes/q1", qTag, `{"total":108}`, nil)
	if err != nil {
		return nil, err
	}
	out = append(out, RaceProbe{"http-target-if-match", code == http.StatusPreconditionFailed, http.StatusText(code)})
	return out, nil
}

func errString(err error) string {
	if err == nil {
		return "accepted"
	}
	return err.Error()
}
