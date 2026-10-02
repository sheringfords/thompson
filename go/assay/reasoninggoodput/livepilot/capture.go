// Capture: observable-boundary session parsing (Phase 3).
//
// Parses OpenCode `--format json` event streams into an ordered tool/read
// ledger WITHOUT touching hidden reasoning: only session/message/part IDs,
// tool names/inputs/outputs, and result text are consumed. Unknown part
// shapes are recorded (not dropped silently) for parser evolution during
// dev; the parser version is frozen before held-out runs and recorded per
// run. Thinking/reasoning parts are never requested and, if ever observed,
// are explicitly excluded from premise analysis.
package livepilot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ParserVersion is bumped whenever capture semantics change. Frozen before
// held-out runs; recorded in every run record.
const ParserVersion = "v1-stream-state"

// Part is one normalized stream event.
type Part struct {
	Seq       int
	Type      string // step-start | text | tool-call | tool-result | unknown
	Tool      string
	InputJSON string
	Output    string
	Raw       string // first 2KB, for unknown-shape inspection
	// T0/T1 bound observed wall time (milliseconds since epoch style).
	// OpenCode emits "timestamp" per event and time:{start,end} per part.
	Stamp int64
	End   int64
}

// Session is a parsed run transcript.
type Session struct {
	SessionID string
	Parts     []Part
	Unknown   []string // distinct unknown type names observed
}

// ParseStream reads an OpenCode JSON event stream (one object per line).
func ParseStream(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{}
	seq := 0
	seenUnknown := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue // non-JSON progress chatter: skip, counted below
		}
		typ, _ := obj["type"].(string)
		part, _ := obj["part"].(map[string]interface{})
		ptype := ""
		if part != nil {
			ptype, _ = part["type"].(string)
		}
		key := typ + "/" + ptype
		p := Part{Seq: seq, Raw: trunc(line, 2048)}
		seq++
		if ts, ok := obj["timestamp"].(float64); ok {
			p.Stamp = int64(ts)
		}
		if part != nil {
			if tm, ok := part["time"].(map[string]interface{}); ok {
				if st, ok := tm["start"].(float64); ok {
					p.Stamp = int64(st)
				}
				if en, ok := tm["end"].(float64); ok {
					p.End = int64(en)
				}
			}
		}
		switch {
		case strings.Contains(key, "step"):
			p.Type = "step-start"
		case ptype == "text" || typ == "text":
			p.Type = "text"
			if part != nil {
				p.Output, _ = part["text"].(string)
			}
		case ptype == "tool" || strings.Contains(typ, "tool"):
			p.Type = "tool-call"
			if part != nil {
				p.Tool, _ = part["tool"].(string)
				if p.Tool == "" {
					p.Tool, _ = part["name"].(string)
				}
				// OpenCode v2 streams tool I/O under part.state
				// ({status, input, output}); legacy flat shape kept too.
				state, _ := part["state"].(map[string]interface{})
				if state != nil {
					if in, ok := state["input"]; ok {
						b, _ := json.Marshal(in)
						p.InputJSON = string(b)
					}
					if out, ok := state["output"].(string); ok {
						p.Output = out
					}
				}
				if p.InputJSON == "" {
					if in, ok := part["input"]; ok {
						b, _ := json.Marshal(in)
						p.InputJSON = string(b)
					}
				}
				if p.Output == "" {
					if out, ok := part["output"].(string); ok {
						p.Output = out
					}
				}
				if p.Tool == "" {
					p.Tool, _ = obj["tool"].(string)
				}
			}
		default:
			p.Type = "unknown"
			if !seenUnknown[key] {
				seenUnknown[key] = true
				s.Unknown = append(s.Unknown, key)
			}
		}
		if id, ok := obj["sessionID"].(string); ok && s.SessionID == "" {
			s.SessionID = id
		}
		s.Parts = append(s.Parts, p)
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	return s, nil
}

// ReadEvents extracts likely file-read tool calls (path-bearing inputs).
// Conservative: any tool call whose input JSON mentions a repo-relative
// .go/.md path counts as a read observation; bytes are recovered by the
// caller from the witnessed blob at read time where possible (see runner).
func (s *Session) ReadEvents() []Part {
	var out []Part
	for _, p := range s.Parts {
		if p.Type != "tool-call" {
			continue
		}
		lower := strings.ToLower(p.Tool + " " + p.InputJSON)
		if strings.Contains(lower, "read") || strings.Contains(lower, ".go") || strings.Contains(lower, ".md") {
			out = append(out, p)
		}
	}
	return out
}

// ModelCalls counts text-producing steps (proxy for model invocations;
// refined once tool-call/result pairing is confirmed on dev runs).
func (s *Session) ModelCalls() int {
	n := 0
	for _, p := range s.Parts {
		if p.Type == "text" && strings.TrimSpace(p.Output) != "" {
			n++
		}
	}
	return n
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var _ = fmt.Sprint
