// Live analysis: from a captured OpenCode session to premise slices,
// SARF, and L1/L2/L3 counterfactuals — offline, no model calls, no hidden
// reasoning. Slices group stream parts by messageID (one model turn each).
// File premises resolve BY CONTENT (blob OIDs recomputed from observed
// bytes, verified against the repo); directory/glob premises are structural
// filename sets derived from output bytes. Costs are step token counts
// (input+output+reasoning; cache tokens tracked separately, never mixed).
package livepilot

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// digestString is the local content-digest helper (stdlib only; the
// livepilot package stays independent of assay hashing helpers).
func digestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// BlobOID recomputes a Git blob OID purely from content bytes
// ("blob <len>\0" + bytes, SHA-1). Identical bytes always yield identical
// OIDs regardless of when they were observed — content addressing removes
// read-timing from witness resolution entirely.
func BlobOID(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// BlobExists checks an OID against a repository object database.
func BlobExists(repoDir, oid string) bool {
	cmd := exec.Command("git", "-C", repoDir, "cat-file", "-e", oid)
	return cmd.Run() == nil
}

// FileRead is one recovered file-content observation: repo-relative path,
// reconstructed bytes, and the content-derived blob OID (empty when the
// bytes match no in-repo blob — caller then marks UNKNOWN).
type FileRead struct {
	Path  string
	Bytes []byte
	OID   string
}

// RecoverReads extracts file-content reads from tool-call parts. Read-tool
// outputs shaped "Read file <path>, lines..." carry `N: <content>` lines;
// stripping the prefixes reconstructs the bytes (trailing-newline variants
// tried in order). Directory outputs yield structural premises instead.
func RecoverReads(s *Session, repoDir, workPrefix string) (files []FileRead, dirs map[string]string) {
	dirs = map[string]string{}
	for _, p := range s.Parts {
		if p.Type != "tool-call" {
			continue
		}
		if !isReadTool(p.Tool) {
			continue
		}
		path := inputPath(p.InputJSON)
		if path == "" {
			continue
		}
		rel := relPath(path, workPrefix)
		out := p.Output
		switch {
		case strings.HasPrefix(out, "Read file "):
			body := stripLineNumbers(out)
			oid := resolveBytes(repoDir, []byte(body))
			files = append(files, FileRead{Path: rel, Bytes: []byte(body), OID: oid})
		case strings.HasPrefix(out, "Read directory "):
			dirs[rel] = dirSetDigest(out)
		}
	}
	return files, dirs
}

func isReadTool(tool string) bool {
	t := strings.ToLower(tool)
	return t == "read" || strings.Contains(t, "read")
}

// inputPath extracts a path-like input field from tool input JSON.
func inputPath(inputJSON string) string {
	for _, key := range []string{`"path":"`, `"file":"`, `"filePath":"`} {
		if i := strings.Index(inputJSON, key); i >= 0 {
			rest := inputJSON[i+len(key):]
			if j := strings.Index(rest, `"`); j >= 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// relPath makes an absolute worktree path repo-relative.
func relPath(abs, prefix string) string {
	rel := strings.TrimPrefix(abs, prefix)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}

// stripLineNumbers removes leading `N: ` prefixes from read-tool output,
// recovering approximate file bytes (trailing newline normalized by the
// caller trying both variants during OID resolution).
func stripLineNumbers(out string) string {
	lines := strings.Split(out, "\n")
	if len(lines) > 0 && (strings.HasPrefix(lines[0], "Read file ") || strings.HasPrefix(lines[0], "Read directory ")) {
		lines = lines[1:]
	}
	for i, l := range lines {
		if j := strings.Index(l, ": "); j >= 0 {
			head := l[:j]
			if _, err := strconv.Atoi(strings.TrimSpace(head)); err == nil {
				lines[i] = l[j+2:]
				continue
			}
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// resolveBytes returns the blob OID when reconstructed bytes match an
// in-repo object (trying with/without trailing newline), else "".
func resolveBytes(repoDir string, body []byte) string {
	for _, v := range [][]byte{body, []byte(strings.TrimSuffix(string(body), "\n") + "\n"), []byte(strings.TrimSuffix(string(body), "\n"))} {
		oid := BlobOID(v)
		if BlobExists(repoDir, oid) {
			return oid
		}
	}
	return ""
}

// dirSetDigest binds a directory listing to its sorted filename set
// (structural premise: stable across content-only changes, moves on
// add/delete/rename). Derived from observed output, never a version claim.
func dirSetDigest(out string) string {
	var names []string
	for _, l := range strings.Split(out, "\n")[1:] {
		n := strings.TrimSpace(strings.TrimSuffix(l, "/"))
		if n != "" && !strings.Contains(n, " ") && !strings.Contains(n, "entries") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return digestString("dirset:" + strings.Join(names, ","))
}

// LiveSlice is one model turn with mechanical premises and token cost.
type LiveSlice struct {
	MessageID string
	Premises  map[string]string // resource -> witness (OID or dirset digest)
	Tokens    int64             // input+output+reasoning tokens this turn
	ToolCalls int
}

// BuildSlices groups parts by messageID, resolves file premises by content
// against repoDir, and prices turns by step token counts. workPrefix scopes
// absolute paths to the worktree. Unresolvable reads yield UNKNOWN premises
// (fail-closed downstream, counted in metrics).
func BuildSlices(s *Session, repoDir, workPrefix string) []LiveSlice {
	type acc struct {
		premises map[string]string
		tokens   int64
		tools    int
	}
	order := []string{}
	groups := map[string]*acc{}
	cur := ""
	for _, p := range s.Parts {
		if mid, ok := partMessage(p); ok && mid != "" {
			cur = mid
		}
		if cur == "" {
			continue
		}
		a, ok := groups[cur]
		if !ok {
			a = &acc{premises: map[string]string{}}
			groups[cur] = a
			order = append(order, cur)
		}
		if p.Type == "tool-call" {
			a.tools++
			path := inputPath(p.InputJSON)
			if path == "" {
				continue
			}
			rel := relPath(path, workPrefix)
			out := p.Output
			switch {
			case strings.HasPrefix(out, "Read file "):
				body := stripLineNumbers(out)
				oid := resolveBytes(repoDir, []byte(body))
				if oid == "" {
					a.premises["file:"+rel] = "UNKNOWN"
				} else {
					a.premises["file:"+rel] = oid
				}
			case strings.HasPrefix(out, "Read directory ") || isGlobTool(p.Tool):
				a.premises["dir:"+relDir(rel)] = dirSetDigest(out)
			}
		}
		if toks := partTokens(p); toks > 0 {
			a.tokens += toks
		}
	}
	var slices []LiveSlice
	for _, mid := range order {
		a := groups[mid]
		slices = append(slices, LiveSlice{MessageID: mid, Premises: a.premises, Tokens: a.tokens, ToolCalls: a.tools})
	}
	// Cumulative union: turn N sees all history (conservative model).
	accum := map[string]string{}
	for i := range slices {
		for k, v := range slices[i].Premises {
			if _, exists := accum[k]; !exists {
				accum[k] = v
			}
		}
		for k, v := range accum {
			slices[i].Premises[k] = v
		}
	}
	return slices
}

func partMessage(p Part) (string, bool) {
	// MessageID is not yet parsed into Part; recover from Raw JSON.
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(p.Raw), &obj); err != nil {
		return "", false
	}
	part, _ := obj["part"].(map[string]interface{})
	if part == nil {
		return "", false
	}
	mid, _ := part["messageID"].(string)
	return mid, mid != ""
}

func isGlobTool(tool string) bool {
	return strings.ToLower(tool) == "glob"
}

func relDir(rel string) string {
	if strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".mod") {
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			return rel[:i]
		}
		return "."
	}
	return rel
}

// partTokens extracts input+output+reasoning token counts from a part.
// Cache tokens are excluded from cost (reported separately, never mixed).
func partTokens(p Part) int64 {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(p.Raw), &obj); err != nil {
		return 0
	}
	part, _ := obj["part"].(map[string]interface{})
	if part == nil {
		return 0
	}
	var total int64
	toks, _ := part["tokens"].(map[string]interface{})
	if toks == nil {
		if objT, ok := obj["tokens"].(map[string]interface{}); ok {
			toks = objT
		}
	}
	for _, k := range []string{"input", "output", "reasoning"} {
		if v, ok := toks[k].(float64); ok {
			total += int64(v)
		}
	}
	return total
}

// CommonRoot derives the worktree root as the longest common directory of
// absolute read paths in the session (all reads live under one worktree).
func CommonRoot(s *Session) string {
	var paths []string
	for _, p := range s.Parts {
		if p.Type != "tool-call" {
			continue
		}
		if ap := inputPath(p.InputJSON); ap != "" && ap[0] == '/' {
			paths = append(paths, ap)
		}
	}
	if len(paths) == 0 {
		return "/"
	}
	root := paths[0]
	up := func(r string) string {
		if i := lastSlash(r); i > 0 {
			return r[:i]
		}
		return "/"
	}
	for _, p := range paths[1:] {
		for root != "/" && p != root && !strings.HasPrefix(p, root+"/") {
			root = up(root)
		}
	}
	extends := false
	for _, p := range paths {
		if p != root && strings.HasPrefix(p, root+"/") {
			extends = true
			break
		}
	}
	if !extends {
		root = up(root)
	}
	return root
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// ChangedPremises maps premise resources to [preOID, postOID] for one
// frozen background change, computed purely from frozen content (base
// fixture bytes vs change bytes). No repository history needed: identical
// bytes always share blob OIDs, so pre/post identities are exact.
func ChangedPremises(changeID string) map[string][2]string {
	out := map[string][2]string{}
	ch, ok := BackgroundChanges()[changeID]
	if !ok {
		return out
	}
	base := map[string]string{}
	for _, f := range FixtureFiles() {
		base[f.Path] = f.Content
	}
	for p, body := range ch.Files {
		pre := BlobOID([]byte(base[p]))
		post := BlobOID([]byte(body))
		if pre != post {
			out["file:"+p] = [2]string{pre, post}
		}
	}
	return out
}
