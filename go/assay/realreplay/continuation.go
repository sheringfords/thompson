package realreplay

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Continuation construction (actual selective replay, observable-only).
//
// Given an exported session and the index of the first stale call, the
// continuation keeps the task message and every assistant message strictly
// before it, and removes everything that is not observable transcript
// state: reasoning parts (provider-encrypted chain of thought) and
// provider item state. Preserved messages are imported, not regenerated:
// no model call is made for them. The resumed agent then performs the
// stale call and everything after it for real.

// hiddenKeys are removed recursively from preserved messages.
var hiddenKeys = []string{"providerState", "providerMetadata", "reasoningEncryptedContent"}

func scrub(v interface{}) (interface{}, int) {
	n := 0
	switch x := v.(type) {
	case map[string]interface{}:
		for _, k := range hiddenKeys {
			if _, ok := x[k]; ok {
				delete(x, k)
				n++
			}
		}
		for k, e := range x {
			var m int
			x[k], m = scrub(e)
			n += m
		}
	case []interface{}:
		out := x[:0]
		for _, e := range x {
			if m, ok := e.(map[string]interface{}); ok && m["type"] == "reasoning" {
				n++
				continue
			}
			s, m := scrub(e)
			n += m
			out = append(out, s)
		}
		return out, n
	}
	return v, n
}

// ContinuationStats records what was kept and removed.
type ContinuationStats struct {
	SourceSession    string   `json:"source_session"`
	NewSession       string   `json:"new_session"`
	KeptAssistant    int      `json:"kept_assistant_messages"`
	DroppedAssistant int      `json:"dropped_assistant_messages"`
	HiddenRemoved    int      `json:"hidden_items_removed"`
	KeptMessageIDs   []string `json:"kept_source_message_ids"`
}

// BuildContinuation writes an importable session JSON to outPath keeping
// the first keep assistant messages of the export at exportPath.
func BuildContinuation(exportPath, outPath, workspace, tag string, keep int) (ContinuationStats, error) {
	var st ContinuationStats
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		return st, err
	}
	var exp map[string]interface{}
	if err := json.Unmarshal(raw, &exp); err != nil {
		return st, err
	}
	info, _ := exp["info"].(map[string]interface{})
	msgs, _ := exp["messages"].([]interface{})
	if info == nil || msgs == nil {
		return st, fmt.Errorf("unrecognized export shape")
	}
	if len(tag) != 4 {
		return st, fmt.Errorf("tag must be 4 chars")
	}
	retag := func(id string) string { return id[:len(id)-4] + tag }
	st.SourceSession, _ = info["id"].(string)
	var kept []interface{}
	assistants := 0
	for _, m := range msgs {
		mm, _ := m.(map[string]interface{})
		typ, _ := mm["type"].(string)
		if typ == "assistant" {
			if assistants >= keep {
				st.DroppedAssistant++
				continue
			}
			assistants++
		} else if assistants >= keep && typ != "" && len(kept) > 0 {
			// Anything after the cut (user/system) is dropped too.
			continue
		}
		id, _ := mm["id"].(string)
		st.KeptMessageIDs = append(st.KeptMessageIDs, id)
		s, n := scrub(mm)
		st.HiddenRemoved += n
		sm := s.(map[string]interface{})
		sm["id"] = retag(id)
		kept = append(kept, sm)
	}
	st.KeptAssistant = assistants
	sid, _ := info["id"].(string)
	st.NewSession = retag(sid)
	info["id"] = st.NewSession
	if loc, ok := info["location"].(map[string]interface{}); ok {
		loc["directory"] = workspace
	}
	delete(info, "title")
	exp["messages"] = kept
	out, err := json.MarshalIndent(exp, "", " ")
	if err != nil {
		return st, err
	}
	if strings.Contains(string(out), "reasoningEncryptedContent") {
		return st, fmt.Errorf("hidden reasoning survived scrub")
	}
	return st, os.WriteFile(outPath, out, 0o644)
}
