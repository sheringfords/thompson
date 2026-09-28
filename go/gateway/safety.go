package gateway

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// Arm safety states (SUPERVISED_PILOT_SAFETY_CONTRACT_V1.md).
type ArmSafetyState string

const (
	// ArmPrequalified: approved, no exposure accounting against it yet.
	ArmPrequalified ArmSafetyState = "PREQUALIFIED"
	// ArmSuspended: excluded from all adaptive selection until authorized resume.
	ArmSuspended ArmSafetyState = "SUSPENDED"
	// ArmDisabled: approval withdrawn; return requires re-approval.
	ArmDisabled ArmSafetyState = "DISABLED"
)

// SafetyConfig freezes the pilot safety envelope. Every field is a charter
// item: changing one restarts the experiment. Tightening-only mid-run rule
// is enforced by config-match on resume, not by this struct.
type SafetyConfig struct {
	Workload             string   `json:"workload"`
	Arms                 []string `json:"arms"`
	FallbackArm          string   `json:"fallback_arm"`
	QualityFloor         float64  `json:"quality_floor"`
	MonitorWindow        int      `json:"monitor_window"`
	MonitorMinObs        int      `json:"monitor_min_obs"`
	MaxExplorationPerArm uint64   `json:"max_exploration_per_arm"`
	MaxMissingShare      float64  `json:"max_missing_share"`
	ColdStartPulls       uint64   `json:"cold_start_pulls"`
}

// Validate rejects unsafe or incoherent envelopes explicitly.
func (c SafetyConfig) Validate() error {
	if c.Workload == "" {
		return fmt.Errorf("safety: workload is required")
	}
	if len(c.Arms) == 0 {
		return fmt.Errorf("safety: at least one prequalified arm is required")
	}
	seen := map[string]bool{}
	for _, a := range c.Arms {
		if a == "" || seen[a] {
			return fmt.Errorf("safety: duplicate or empty arm %q", a)
		}
		seen[a] = true
	}
	if !seen[c.FallbackArm] {
		return fmt.Errorf("safety: fallback arm %q is not prequalified", c.FallbackArm)
	}
	if !(c.QualityFloor >= 0 && c.QualityFloor <= 1) {
		return fmt.Errorf("safety: quality floor %v out of [0,1]", c.QualityFloor)
	}
	if c.MonitorWindow <= 0 || c.MonitorMinObs <= 0 || c.MonitorMinObs > c.MonitorWindow {
		return fmt.Errorf("safety: need 0 < min_obs <= window, got %d/%d", c.MonitorMinObs, c.MonitorWindow)
	}
	if c.MaxExplorationPerArm < c.ColdStartPulls {
		return fmt.Errorf("safety: exploration budget %d below cold-start pulls %d (arm could never exit cold)",
			c.MaxExplorationPerArm, c.ColdStartPulls)
	}
	if !(c.MaxMissingShare >= 0 && c.MaxMissingShare <= 1) {
		return fmt.Errorf("safety: max missing share %v out of [0,1]", c.MaxMissingShare)
	}
	return nil
}

// SafetyEvent is one durable safety-state transition. The event log is
// authoritative; restarts re-fold it before serving traffic.
type SafetyEvent struct {
	Seq        uint64 `json:"seq"`
	At         string `json:"at"`
	Actor      string `json:"actor"`
	Type       string `json:"type"`
	Arm        string `json:"arm,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	ConfigHash string `json:"config_hash,omitempty"`
}

const (
	SafetyApprove          = "APPROVE"
	SafetyArmSuspended     = "ARM_SUSPENDED"
	SafetyArmResumed       = "ARM_RESUMED"
	SafetyArmDisabled      = "ARM_DISABLED"
	SafetyEmergencyStop    = "EMERGENCY_STOP"
	SafetyEmergencyRelease = "EMERGENCY_RELEASED"
)

// armSafety is live per-arm state folded from events plus budget counters.
type armSafety struct {
	approved   bool
	state      ArmSafetyState
	reason     string
	at         string
	explored   uint64
	suspendSeq uint64
}

// SafetyEventSink persists safety-transition events durably. Implemented
// by *SafetyStore (file log) and by journal-backed adapters; the
// controller only depends on this seam.
type SafetyEventSink interface {
	Append(SafetyEvent) error
	Events() ([]SafetyEvent, error)
	Failed() bool
	Close() error
}

// SafetyController enforces the safety contract. It implements SafetyGate
// for selection and SettlementObserver for monitoring-driven suspension.
// Locking: one mutex guards everything. Established partial order:
// settleMu > policy/book/outcome-store locks > safetyMu. Selection takes
// policy/book first, then safety; settlement takes settleMu, then
// policy/book/store, then safety. Safety code never acquires an outer lock
// while held (event slices are passed in; stores are only read).
type SafetyController struct {
	mu        sync.Mutex
	cfg       SafetyConfig
	cfgHash   string
	arms      map[string]*armSafety
	emerg     bool
	store     SafetyEventSink
	decisions DecisionStore
	outcomes  outcome.OutcomeStore
}

// NewSafetyController builds the controller over a durable event store,
// re-folding history and replaying exploration budgets from the decisions
// ledger. Decisions must implement DecisionScanner when non-empty; otherwise
// resume refuses (budgets cannot be reconstructed).
func NewSafetyController(cfg SafetyConfig, cfgHash string, store SafetyEventSink, decisions DecisionStore, outcomes outcome.OutcomeStore) (*SafetyController, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("safety: event store is required (fail closed, never in-memory-by-default)")
	}
	c := &SafetyController{cfg: cfg, cfgHash: cfgHash, arms: map[string]*armSafety{}, store: store, decisions: decisions, outcomes: outcomes}
	for _, a := range cfg.Arms {
		c.arms[a] = &armSafety{approved: true, state: ArmPrequalified}
	}
	if err := c.recover(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *SafetyController) recover() error {
	evs, err := c.store.Events()
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if ev.ConfigHash != "" && ev.ConfigHash != c.cfgHash {
			return fmt.Errorf("safety: event %d config %q != frozen %q (refusing resume across config change)", ev.Seq, ev.ConfigHash, c.cfgHash)
		}
		c.fold(ev)
	}
	// Exploration budgets replay from committed decisions (authoritative):
	// every cold/fallback exploration pick ever committed counts.
	if c.decisions != nil && c.decisions.Len() > 0 {
		sc, ok := c.decisions.(DecisionScanner)
		if !ok {
			return fmt.Errorf("safety: decisions store holds %d rows but cannot scan (budgets unrecoverable)", c.decisions.Len())
		}
		if err := sc.Scan(func(d CommittedDecision) bool {
			if d.LoggingPolicyID != "thompson-costaware-v1" || d.CostAware == nil {
				return true
			}
			st, ok := c.arms[d.SelectedArmID]
			if !ok {
				return true
			}
			if d.CostAware.Fallback {
				st.explored++
			}
			return true
		}); err != nil {
			return fmt.Errorf("safety: decisions replay: %w", err)
		}
	}
	return nil
}

func (c *SafetyController) fold(ev SafetyEvent) {
	switch ev.Type {
	case SafetyArmSuspended:
		if st, ok := c.arms[ev.Arm]; ok {
			st.state, st.reason, st.at, st.suspendSeq = ArmSuspended, ev.Reason, ev.At, ev.Seq
		}
	case SafetyArmResumed:
		if st, ok := c.arms[ev.Arm]; ok && st.state == ArmSuspended {
			st.state, st.reason = ArmPrequalified, ""
		}
	case SafetyArmDisabled:
		if st, ok := c.arms[ev.Arm]; ok {
			st.state, st.reason, st.at = ArmDisabled, ev.Reason, ev.At
		}
	case SafetyEmergencyStop:
		c.emerg = true
	case SafetyEmergencyRelease:
		c.emerg = false
	}
}

func (c *SafetyController) appendLocked(ev SafetyEvent) error {
	ev.At = time.Now().UTC().Format(time.RFC3339Nano)
	ev.ConfigHash = c.cfgHash
	if err := c.store.Append(ev); err != nil {
		return err
	}
	evs, err := c.store.Events()
	if err != nil {
		return err
	}
	c.fold(evs[len(evs)-1])
	return nil
}

// Authorize implements SafetyGate: permitted arms now. Suspended/disabled
// arms are out; emergency stop admits the fallback only (when itself
// usable); a failed safety store admits fallback-or-nothing (fail closed,
// never blind adaptation).
func (c *SafetyController) Authorize(eligible []string) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool)
	if c.store.Failed() {
		if st, ok := c.arms[c.cfg.FallbackArm]; ok && st.state != ArmSuspended && st.state != ArmDisabled {
			out[c.cfg.FallbackArm] = true
		}
		return out
	}
	for _, a := range eligible {
		st, ok := c.arms[a]
		if !ok || !st.approved || st.state == ArmSuspended || st.state == ArmDisabled {
			continue
		}
		if c.emerg && a != c.cfg.FallbackArm {
			continue
		}
		out[a] = true
	}
	return out
}

// Reserve implements SafetyGate: genuine optima only account; exploration
// consumes the durable budget and auto-suspends on exhaustion (persisted,
// surviving restart; no refill without a new experiment).
func (c *SafetyController) Reserve(arm string, explore bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.arms[arm]
	if !ok || !st.approved || st.state == ArmSuspended || st.state == ArmDisabled {
		return false
	}
	if c.store.Failed() {
		return arm == c.cfg.FallbackArm && !explore
	}
	if !explore {
		return true
	}
	if st.explored >= c.cfg.MaxExplorationPerArm {
		_ = c.appendLocked(SafetyEvent{Actor: "monitor", Type: SafetyArmSuspended, Arm: arm,
			Reason:   fmt.Sprintf("exploration budget exhausted (%d/%d)", st.explored, c.cfg.MaxExplorationPerArm),
			Evidence: fmt.Sprintf(`{"explored":%d,"budget":%d}`, st.explored, c.cfg.MaxExplorationPerArm)})
		return false
	}
	st.explored++
	return true
}

// ObserveSettlement implements SettlementObserver: fold the event into
// monitoring and suspend arms breaching frozen limits. Called under the
// router settlement mutex after durable commit.
func (c *SafetyController) ObserveSettlement(ev outcome.OutcomeEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store.Failed() {
		return fmt.Errorf("safety: event store failed (refusing blind operation)")
	}
	for _, arm := range c.monitorArms(ev) {
		h := armHealth(c.outcomes.Events(), arm, c.cfg.MonitorWindow)
		if h.Matured < c.cfg.MonitorMinObs {
			continue
		}
		if h.AcceptRate < c.cfg.QualityFloor {
			if err := c.suspendLocked(arm, "monitor",
				fmt.Sprintf("accept rate %.3f below floor %.3f over %d matured", h.AcceptRate, c.cfg.QualityFloor, h.Matured),
				healthJSON(h)); err != nil {
				return err
			}
			continue
		}
		if share := missingShare(h); share > c.cfg.MaxMissingShare {
			if err := c.suspendLocked(arm, "monitor",
				fmt.Sprintf("missing-cost share %.3f above budget %.3f over %d matured", share, c.cfg.MaxMissingShare, h.Matured),
				healthJSON(h)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *SafetyController) monitorArms(ev outcome.OutcomeEvent) []string {
	set := map[string]bool{}
	for _, a := range ev.Attempts {
		if a.ArmID != "" {
			set[a.ArmID] = true
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		if _, ok := c.arms[a]; ok {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

func (c *SafetyController) suspendLocked(arm, actor, reason, evidence string) error {
	st, ok := c.arms[arm]
	if !ok || st.state == ArmSuspended || st.state == ArmDisabled {
		return nil // idempotent: already out
	}
	return c.appendLocked(SafetyEvent{Actor: actor, Type: SafetyArmSuspended, Arm: arm, Reason: reason, Evidence: evidence})
}

// Suspend operator-suspends one arm (or all with arm=""): persisted before
// effect; idempotent.
func (c *SafetyController) Suspend(actor, arm, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if arm == "" {
		for a := range c.arms {
			if err := c.suspendLocked(a, actor, reason, ""); err != nil {
				return err
			}
		}
		return c.appendLocked(SafetyEvent{Actor: actor, Type: SafetyEmergencyStop, Reason: reason})
	}
	if _, ok := c.arms[arm]; !ok {
		return fmt.Errorf("safety: unknown arm %q", arm)
	}
	return c.suspendLocked(arm, actor, reason, "")
}

// Resume re-admits a suspended arm. Requires a reason; disabled arms and
// emergency state need their own paths (re-approval / release).
func (c *SafetyController) Resume(actor, arm, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason == "" {
		return fmt.Errorf("safety: resume requires a reason")
	}
	st, ok := c.arms[arm]
	if !ok {
		return fmt.Errorf("safety: unknown arm %q", arm)
	}
	if st.state == ArmDisabled {
		return fmt.Errorf("safety: arm %q is disabled (re-approval required, resume refused)", arm)
	}
	if st.state != ArmSuspended {
		return fmt.Errorf("safety: arm %q is not suspended", arm)
	}
	return c.appendLocked(SafetyEvent{Actor: actor, Type: SafetyArmResumed, Arm: arm, Reason: reason})
}

// Disable withdraws approval; Reapprove restores PREQUALIFIED without
// refunding the exploration budget.
func (c *SafetyController) Disable(actor, arm, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.arms[arm]; !ok {
		return fmt.Errorf("safety: unknown arm %q", arm)
	}
	return c.appendLocked(SafetyEvent{Actor: actor, Type: SafetyArmDisabled, Arm: arm, Reason: reason})
}

// EmergencyStop halts all adaptive selection (fallback only); Release
// restores the pre-stop mask. Both persisted and restart-durable.
func (c *SafetyController) EmergencyStop(actor, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appendLocked(SafetyEvent{Actor: actor, Type: SafetyEmergencyStop, Reason: reason})
}

func (c *SafetyController) EmergencyRelease(actor, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason == "" {
		return fmt.Errorf("safety: release requires a reason")
	}
	return c.appendLocked(SafetyEvent{Actor: actor, Type: SafetyEmergencyRelease, Reason: reason})
}

// State snapshots one arm for operators and tests.
func (c *SafetyController) State(arm string) (ArmSafetyState, uint64, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.arms[arm]
	if !ok {
		return "", 0, "unknown arm"
	}
	return st.state, st.explored, st.reason
}

func (c *SafetyController) Emergency() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.emerg
}

func healthJSON(h ArmHealth) string {
	b, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(b)
}
