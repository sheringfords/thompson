package realreplay

// Metrics and the frozen decision gates (PROTOCOL.md §Gates). All work
// figures are provider/OpenCode-reported token counts of calls that were
// actually executed. Nothing is counterfactual, priced, or estimated.

// TreatmentWork aggregates the executed calls of one treatment.
type TreatmentWork struct {
	Calls        int    `json:"model_calls"`
	ToolCalls    int    `json:"tool_calls"`
	All          Tokens `json:"tokens_all_calls"`
	ExclFirst    Tokens `json:"tokens_excluding_fresh_first_calls"`
	FreshStarts  int    `json:"fresh_starts"`
	UsageMissing int    `json:"calls_without_usage"`
	WallMS       int64  `json:"agent_wall_ms"`
}

// addSegment adds one executed call sequence. fresh marks a run that began
// from the task prompt alone: its first call is the initial prefill and is
// excluded from ExclFirst.
func (w *TreatmentWork) addSegment(calls []*Call, fresh bool, wallMS int64) {
	for i, c := range calls {
		w.Calls++
		w.ToolCalls += len(c.Tools)
		if !c.Usage {
			w.UsageMissing++
		}
		w.All.Add(c.Tokens)
		if !(fresh && i == 0) {
			w.ExclFirst.Add(c.Tokens)
		}
	}
	if fresh {
		w.FreshStarts++
	}
	w.WallMS += wallMS
}

// Scenario is one paired comparison.
type Scenario struct {
	ID         string `json:"id"`
	Family     string `json:"family"` // git | http
	Qualifying bool   `json:"qualifying"`
	Mutation   string `json:"mutation"`

	S0Calls    int    `json:"s0_calls"`
	FirstStale int    `json:"first_stale_call"` // -1 none
	Preserved  int    `json:"preserved_calls"`  // S0 calls reused without re-invocation
	PostFirst  int    `json:"preserved_post_first_calls"`
	R2Mode     string `json:"r2_mode"` // resume | commit-only | restart-equivalent | fallback-restart
	R2Rounds   int    `json:"r2_replay_rounds"`

	R1 TreatmentWork `json:"r1_full_restart"`
	R2 TreatmentWork `json:"r2_selective_replay"`

	R0Oracle        bool     `json:"r0_oracle_pass"`
	R2Oracle        bool     `json:"r2_oracle_pass"`
	R1Oracle        bool     `json:"r1_oracle_pass"`
	StaleS0OnS1Fail bool     `json:"stale_s0_result_fails_s1_oracle"`
	R2Committed     bool     `json:"r2_committed"`
	R0Committed     bool     `json:"r0_committed"`
	MissedStale     int      `json:"missed_stale_premises"`
	UnknownPremises int      `json:"unknown_premises"`
	Notes           []string `json:"notes,omitempty"`

	Discard DiscardClass `json:"discard_classification"`
}

// DiscardClass classifies discarded S0 calls by why they were discarded.
type DiscardClass struct {
	R2True   int `json:"r2_true_dependency"` // a precise premise before it changed
	R2Opaque int `json:"r2_opaque_forced"`   // only opaque/unknown premises forced it
	R1True   int `json:"r1_true_dependency"`
	R1False  int `json:"r1_false_conflict"` // broad OCC discarded; no precise stale premise and no opaque one
	R1Opaque int `json:"r1_opaque_forced"`
}

// Reduction is 1 - a/b (0 when b == 0).
func Reduction(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return 1 - float64(a)/float64(b)
}

// Gates is the frozen gate evaluation for one scenario.
type Gates struct {
	PrimaryReduction   float64 `json:"primary_new_work_reduction_excl_first"`
	PrimaryPass        bool    `json:"primary_pass"`
	InclFirstReduction float64 `json:"new_work_reduction_incl_first"`
	RecoveryReduction  float64 `json:"recovery_only_new_work_reduction_excl_first"`
	FewerReplayedCalls bool    `json:"fewer_repeated_calls"`
	VisibleRatio       float64 `json:"visible_tokens_r2_over_r1"`
	VisibleOK          bool    `json:"visible_within_10pct"`
	PreservedPostFirst bool    `json:"preserves_post_first_call"`
	Correct            bool    `json:"correctness"`
	UsageComplete      bool    `json:"usage_complete"`
	Win                bool    `json:"win"`
}

// Evaluate applies the frozen gates. Primary: R2 executes >= 30% less
// new model work than R1, excluding every fresh first call. A win also
// requires every secondary gate and full correctness.
func Evaluate(s *Scenario, s0, r0, c []*Call) Gates {
	var g Gates
	g.PrimaryReduction = Reduction(s.R2.ExclFirst.NewWork(), s.R1.ExclFirst.NewWork())
	g.PrimaryPass = g.PrimaryReduction >= 0.30
	g.InclFirstReduction = Reduction(s.R2.All.NewWork(), s.R1.All.NewWork())
	var r0x, cx Tokens
	for i, k := range r0 {
		if i > 0 {
			r0x.Add(k.Tokens)
		}
	}
	for _, k := range c {
		cx.Add(k.Tokens)
	}
	g.RecoveryReduction = Reduction(cx.NewWork(), r0x.NewWork())
	g.FewerReplayedCalls = len(c) < len(r0)
	if v := s.R1.All.Visible(); v > 0 {
		g.VisibleRatio = float64(s.R2.All.Visible()) / float64(v)
	}
	g.VisibleOK = g.VisibleRatio <= 1.10
	g.PreservedPostFirst = s.PostFirst >= 1
	g.Correct = s.R2Oracle && s.R0Oracle && s.R2Committed && s.MissedStale == 0
	g.UsageComplete = s.R1.UsageMissing == 0 && s.R2.UsageMissing == 0
	g.Win = s.Qualifying && g.PrimaryPass && g.FewerReplayedCalls && g.VisibleOK &&
		g.PreservedPostFirst && g.Correct && g.UsageComplete
	return g
}

// Decision states (mission phase 13).
const (
	DecisionContinue     = "CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE"
	DecisionGuardOnly    = "KEEP_GUARD_ONLY"
	DecisionArchitecture = "ARCHITECTURE_DEPENDENT"
	DecisionKill         = "KILL_INCREMENTAL_REPLAY_ECONOMICS"
	DecisionTelemetry    = "INCONCLUSIVE_USAGE_TELEMETRY"
)

// Decide applies the frozen decision procedure (PROTOCOL.md §Decision).
// A qualifying scenario counts as a win only if every executed repetition
// of it wins. guardOK is the guard evidence: every injected semantic
// change was detected stale at validation and every commit-race probe was
// rejected by the authority.
//
//  1. any call without usage telemetry           -> INCONCLUSIVE_USAGE_TELEMETRY
//  2. any stale accept or R2/R0 correctness fail -> KILL_INCREMENTAL_REPLAY_ECONOMICS
//  3. a qualifying win in both Git and HTTP      -> CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE
//  4. guardOK and some qualifying scenario has primary reduction > 0 and
//     >= 1 preserved post-first call             -> KEEP_GUARD_ONLY
//  5. otherwise                                  -> KILL_INCREMENTAL_REPLAY_ECONOMICS
func Decide(scs []Scenario, gates []Gates, guardOK bool) (string, []string) {
	var why []string
	byID := map[string][]int{}
	var order []string
	for i, s := range scs {
		if _, ok := byID[s.ID]; !ok {
			order = append(order, s.ID)
		}
		byID[s.ID] = append(byID[s.ID], i)
		if !gates[i].UsageComplete {
			return DecisionTelemetry, []string{s.ID + ": usage telemetry missing on some call"}
		}
		if s.MissedStale > 0 {
			return DecisionKill, []string{s.ID + ": stale premise reached an accepted result"}
		}
		if !gates[i].Correct {
			why = append(why, s.ID+": correctness gate failed")
		}
	}
	if len(why) > 0 {
		return DecisionKill, append(why, "correct selective replay not completed in every scenario")
	}
	winGit, winHTTP, partial := false, false, false
	for _, id := range order {
		idx := byID[id]
		all := true
		for _, i := range idx {
			if !gates[i].Win {
				all = false
			}
			if scs[i].Qualifying && gates[i].PrimaryReduction > 0 && gates[i].PreservedPostFirst {
				partial = true
			}
		}
		if all && scs[idx[0]].Qualifying {
			if scs[idx[0]].Family == "git" {
				winGit = true
			} else {
				winHTTP = true
			}
		}
	}
	why = append(why, "qualifying wins: git="+boolStr(winGit)+" http="+boolStr(winHTTP))
	switch {
	case winGit && winHTTP:
		return DecisionContinue, why
	case guardOK && partial:
		return DecisionGuardOnly, append(why, "guard holds; replay benefit present but insufficient or single-family")
	default:
		if guardOK {
			why = append(why, "guard holds (retain Guard mode); no qualifying post-first-call preservation with positive reduction")
		}
		return DecisionKill, why
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
