package realreplay

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

// Premise kinds. Every witness is a native identity: a Git blob/tree OID or
// an HTTP strong ETag. Unknown is the conservative class for observations
// whose dependency cannot be mechanically resolved; it never validates.
const (
	KindGit     = "git"
	KindHTTP    = "http"
	KindUnknown = "unknown"
)

// Premise is one dependency of a model call's context on external state.
type Premise struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`     // repo-relative path ("" = root) or HTTP path
	Witness string `json:"witness"` // OID / ETag observed (at read time)
	Call    int    `json:"call"`    // index of the call whose tool produced it
	Tool    string `json:"tool"`
	Why     string `json:"why,omitempty"`
}

// World binds the authorities a run observed.
type World struct {
	Workspace string         // absolute workspace path
	Git       *Authority     // authority for workspace files
	Base      string         // commit the workspace was materialized at
	HTTP      *HTTPAuthority // nil for Git-only families
	HTTPLog   []ServedRead   // served-read log covering the run
	// HTTPStart is path->ETag at run start: the conservative witness for
	// opaque observations, whose served version is not attributable.
	HTTPStart map[string]string
}

// internalTools touch only agent-local state (plans, todos), never
// external resources.
var internalTools = map[string]bool{"todowrite": true, "todoread": true, "todo": true}

// Premises returns every premise produced by the tool results of calls,
// with call indices offset by start.
func (w *World) Premises(calls []*Call, start int) ([]Premise, error) {
	var out []Premise
	for i, c := range calls {
		for _, tu := range c.Tools {
			ps, err := w.toolPremises(tu)
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				p.Call = start + i
				p.Tool = tu.Tool
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func str(m map[string]interface{}, k string) string {
	s, _ := m[k].(string)
	return s
}

// rel maps a tool path to a workspace-relative path; ok=false if outside.
func (w *World) rel(p string) (string, bool) {
	if p == "" {
		return "", true
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.Workspace, p)
	}
	r, err := filepath.Rel(w.Workspace, filepath.Clean(p))
	if err != nil || strings.HasPrefix(r, "..") {
		return "", false
	}
	if r == "." {
		r = ""
	}
	return filepath.ToSlash(r), true
}

func (w *World) namesPremise(dir string, recursive bool) (Premise, error) {
	wit, err := w.Git.NamesWitness(w.Base, dir, recursive)
	key := "names:" + dir
	if recursive {
		key = "names-r:" + dir
	}
	return Premise{Kind: KindGit, Key: key, Witness: wit}, err
}

func (w *World) gitPremise(path string) (Premise, error) {
	oid, err := w.Git.Witness(w.Base, path)
	if err != nil {
		return Premise{}, err
	}
	return Premise{Kind: KindGit, Key: path, Witness: oid}, nil
}

// staticDir returns the directory prefix of a glob pattern before its first
// wildcard segment.
func staticDir(pattern string) string {
	segs := strings.Split(filepath.ToSlash(pattern), "/")
	var keep []string
	for _, s := range segs {
		if strings.ContainsAny(s, "*?[{") {
			break
		}
		keep = append(keep, s)
	}
	if len(keep) == len(segs) && len(keep) > 0 {
		keep = keep[:len(keep)-1] // literal path: its parent directory
	}
	return strings.Join(keep, "/")
}

// opaqueWorld is the conservative premise set of an observation whose
// dependency structure is unknown: the whole workspace tree plus every
// HTTP resource.
func (w *World) opaqueWorld(why string) ([]Premise, error) {
	root, err := w.gitPremise("")
	if err != nil {
		return nil, err
	}
	root.Why = why
	out := []Premise{root}
	keys := make([]string, 0, len(w.HTTPStart))
	for p := range w.HTTPStart {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		out = append(out, Premise{Kind: KindHTTP, Key: p, Witness: w.HTTPStart[p], Why: why})
	}
	return out, nil
}

func (w *World) toolPremises(tu ToolUse) ([]Premise, error) {
	in := tu.Input
	if in == nil {
		in = map[string]interface{}{}
	}
	switch tu.Tool {
	case "read", "list", "ls":
		r, ok := w.rel(str(in, "path"))
		if !ok {
			return []Premise{{Kind: KindUnknown, Key: str(in, "path"), Why: "outside workspace"}}, nil
		}
		if w.Git.IsTree(w.Base, r) {
			p, err := w.namesPremise(r, false)
			return []Premise{p}, err
		}
		p, err := w.gitPremise(r)
		return []Premise{p}, err
	case "edit", "write", "multiedit", "patch":
		// Read dependency (edit) and write-set (write): both pin the base
		// blob so a concurrent change to the same path is never overwritten.
		r, ok := w.rel(str(in, "path"))
		if !ok {
			return []Premise{{Kind: KindUnknown, Key: str(in, "path"), Why: "outside workspace"}}, nil
		}
		p, err := w.gitPremise(r)
		return []Premise{p}, err
	case "glob", "grep":
		// glob exposes names only (recursive names projection); grep
		// exposes content (content tree OID).
		base := str(in, "path")
		dir := base
		if tu.Tool == "glob" {
			dir = filepath.Join(base, staticDir(str(in, "pattern")))
		}
		r, ok := w.rel(dir)
		if !ok {
			return []Premise{{Kind: KindUnknown, Key: dir, Why: "outside workspace"}}, nil
		}
		if tu.Tool == "glob" {
			p, err := w.namesPremise(r, true)
			return []Premise{p}, err
		}
		p, err := w.gitPremise(r)
		return []Premise{p}, err
	case "webfetch", "fetch":
		return w.httpPremise(tu)
	case "shell", "bash":
		return w.opaqueWorld("shell is opaque")
	default:
		if internalTools[tu.Tool] {
			return nil, nil
		}
		return []Premise{{Kind: KindUnknown, Key: tu.Tool, Why: "unclassified tool"}}, nil
	}
}

// httpPremise binds a fetch to the authority's served-read log: the entry
// for the same path served inside the tool's time window. No match or
// conflicting versions => Unknown (never validates).
func (w *World) httpPremise(tu ToolUse) ([]Premise, error) {
	raw := str(tu.Input, "url")
	u, err := url.Parse(raw)
	if err != nil || w.HTTP == nil || u.Host != w.HTTP.Addr {
		return []Premise{{Kind: KindUnknown, Key: raw, Why: "unwitnessed external URL"}}, nil
	}
	tags := map[string]bool{}
	for _, e := range w.HTTPLog {
		if e.Path == u.Path && e.AtMS >= tu.StartMS-1000 && e.AtMS <= tu.EndMS+1000 {
			tags[e.ETag] = true
		}
	}
	if len(tags) != 1 {
		return []Premise{{Kind: KindUnknown, Key: u.Path, Why: fmt.Sprintf("%d served versions in window", len(tags))}}, nil
	}
	for t := range tags {
		return []Premise{{Kind: KindHTTP, Key: u.Path, Witness: t}}, nil
	}
	return nil, nil
}

// Validation is the result of re-reading authoritative witnesses.
type Validation struct {
	Head       string    `json:"git_head,omitempty"`
	Stale      []Premise `json:"stale"`
	Unknown    []Premise `json:"unknown"`
	FirstStale int       `json:"first_stale_call"` // -1 if none
	Checked    int       `json:"checked"`
}

// Validate compares each premise to the authorities' current state.
func Validate(ps []Premise, git *Authority, h *HTTPAuthority) (Validation, error) {
	v := Validation{FirstStale: -1}
	head := ""
	if git != nil {
		var err error
		if head, err = git.Main(); err != nil {
			return v, err
		}
		v.Head = head
	}
	cache := map[string]string{}
	for _, p := range ps {
		v.Checked++
		stale := false
		switch p.Kind {
		case KindGit:
			cur, ok := cache[p.Key]
			if !ok {
				var err error
				switch {
				case strings.HasPrefix(p.Key, "names:"):
					cur, err = git.NamesWitness(head, strings.TrimPrefix(p.Key, "names:"), false)
				case strings.HasPrefix(p.Key, "names-r:"):
					cur, err = git.NamesWitness(head, strings.TrimPrefix(p.Key, "names-r:"), true)
				default:
					cur, err = git.Witness(head, p.Key)
				}
				if err != nil {
					return v, err
				}
				cache[p.Key] = cur
			}
			stale = cur != p.Witness
		case KindHTTP:
			_, cur, _ := h.Current(p.Key)
			stale = cur != p.Witness
		default:
			v.Unknown = append(v.Unknown, p)
			stale = true
		}
		if stale {
			if p.Kind != KindUnknown {
				v.Stale = append(v.Stale, p)
			}
			if v.FirstStale < 0 || p.Call < v.FirstStale {
				v.FirstStale = p.Call
			}
		}
	}
	sort.Slice(v.Stale, func(i, j int) bool { return v.Stale[i].Call < v.Stale[j].Call })
	return v, nil
}
