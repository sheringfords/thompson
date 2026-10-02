package realreplay

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// Tokens is provider/OpenCode-reported usage for one model call, raw.
// Input is uncached input; CacheRead is input served from the prompt
// cache; CacheWrite is cache creation. Nothing here is estimated.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	// ReasoningExposed is false when the provider omitted the field.
	ReasoningExposed bool `json:"reasoning_exposed"`
}

// NewWork is uncached input + output + reasoning (reasoning omitted, not
// guessed, when unexposed).
func (t Tokens) NewWork() int64 { return t.Input + t.Output + t.Reasoning }

// Visible is every reported token dimension including cached input.
func (t Tokens) Visible() int64 {
	return t.Input + t.CacheRead + t.CacheWrite + t.Output + t.Reasoning
}

// Add accumulates.
func (t *Tokens) Add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.Reasoning += o.Reasoning
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// ToolUse is one observable tool invocation and its result.
type ToolUse struct {
	Tool    string                 `json:"tool"`
	Input   map[string]interface{} `json:"input"`
	Output  string                 `json:"output"`
	Status  string                 `json:"status"`
	StartMS int64                  `json:"start_ms"`
	EndMS   int64                  `json:"end_ms"`
}

// Call is one model invocation (one assistant message / step) with the
// tool results produced after it.
type Call struct {
	MessageID string    `json:"message_id"`
	Steps     int       `json:"steps"`
	Finish    string    `json:"finish"`
	Snapshot  string    `json:"snapshot"` // OpenCode worktree snapshot at step start
	Tokens    Tokens    `json:"tokens"`
	Usage     bool      `json:"usage_reported"`
	Tools     []ToolUse `json:"tools"`
	StartMS   int64     `json:"start_ms"`
	EndMS     int64     `json:"end_ms"`
}

// Stream is a parsed `opencode run --format json` event stream.
type Stream struct {
	SessionID string   `json:"session_id"`
	Calls     []*Call  `json:"calls"`
	Unknown   []string `json:"unknown_event_types"`
	Errors    []string `json:"errors"`
}

func num(m map[string]interface{}, k string) (int64, bool) {
	v, ok := m[k].(float64)
	return int64(v), ok
}

// ParseStream reads the JSONL stream. Only observable fields are consumed:
// IDs, tool inputs/outputs, usage numbers. Reasoning text is never read.
func ParseStream(path string) (*Stream, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Stream{}
	byMsg := map[string]*Call{}
	seenUnknown := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		typ, _ := ev["type"].(string)
		if sid, ok := ev["sessionID"].(string); ok && s.SessionID == "" {
			s.SessionID = sid
		}
		ts, _ := num(ev, "timestamp")
		part, _ := ev["part"].(map[string]interface{})
		if typ == "error" {
			b, _ := json.Marshal(ev["error"])
			s.Errors = append(s.Errors, string(b))
			continue
		}
		if part == nil {
			if !seenUnknown[typ] {
				seenUnknown[typ] = true
				s.Unknown = append(s.Unknown, typ)
			}
			continue
		}
		mid, _ := part["messageID"].(string)
		c := byMsg[mid]
		if c == nil && mid != "" {
			c = &Call{MessageID: mid, StartMS: ts}
			byMsg[mid] = c
			s.Calls = append(s.Calls, c)
		}
		if c == nil {
			continue
		}
		if ts > c.EndMS {
			c.EndMS = ts
		}
		switch typ {
		case "step_start":
			c.Steps++
			if c.Snapshot == "" {
				c.Snapshot, _ = part["snapshot"].(string)
			}
		case "step_finish":
			c.Finish, _ = part["reason"].(string)
			if tk, ok := part["tokens"].(map[string]interface{}); ok {
				c.Usage = true
				var t Tokens
				t.Input, _ = num(tk, "input")
				t.Output, _ = num(tk, "output")
				t.Reasoning, t.ReasoningExposed = num(tk, "reasoning")
				if cache, ok := tk["cache"].(map[string]interface{}); ok {
					t.CacheRead, _ = num(cache, "read")
					t.CacheWrite, _ = num(cache, "write")
				}
				exposed := c.Tokens.ReasoningExposed || t.ReasoningExposed
				c.Tokens.Add(t)
				c.Tokens.ReasoningExposed = exposed
			}
		case "tool_use":
			tu := ToolUse{StartMS: ts, EndMS: ts}
			tu.Tool, _ = part["tool"].(string)
			if st, ok := part["state"].(map[string]interface{}); ok {
				tu.Status, _ = st["status"].(string)
				tu.Input, _ = st["input"].(map[string]interface{})
				tu.Output, _ = st["output"].(string)
				if tm, ok := st["time"].(map[string]interface{}); ok {
					if v, ok := num(tm, "start"); ok {
						tu.StartMS = v
					}
					if v, ok := num(tm, "end"); ok {
						tu.EndMS = v
					}
				}
			}
			c.Tools = append(c.Tools, tu)
		case "text", "reasoning":
			// Observable text is not needed for premises; reasoning is
			// never inspected.
		default:
			if !seenUnknown[typ] {
				seenUnknown[typ] = true
				s.Unknown = append(s.Unknown, typ)
			}
		}
	}
	return s, sc.Err()
}

// Sum totals usage over calls.
func Sum(calls []*Call) Tokens {
	var t Tokens
	for _, c := range calls {
		t.Add(c.Tokens)
	}
	return t
}
