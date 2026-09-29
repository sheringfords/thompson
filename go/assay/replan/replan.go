// Package replan implements the runtime-replanning assay prototype
// (THOMPSON_RUNTIME_REPLANNING_ASSAY_V1, Phases 2-4): a bounded, deterministic
// runtime that re-quotes and switches remaining physical plans when
// authoritative mid-run evidence arrives. It reuses go/assay/reuse and
// go/assay/plan unchanged (plus small additive plan APIs); it never modifies
// VerifiedArtifact V1 semantics. Research harness only.
package replan

import (
	"fmt"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// EffectClass classifies assay operations (Phase 2).
type EffectClass string

const (
	// PURE nodes may be abandoned or replaced freely when unneeded.
	EffectPure EffectClass = "PURE"
	// EffectIdempotent nodes apply an external effect exactly once per
	// computation key; retry under the same key dedups, never double-applies.
	EffectIdempotent EffectClass = "IDEMPOTENT_EFFECT"
	// EffectIrreversible nodes create a hard historical boundary: later plans
	// must be compatible with the effect having occurred.
	EffectIrreversible EffectClass = "IRREVERSIBLE_EFFECT"
)

// EffectSpec binds an op to its class + external scope.
type EffectSpec struct {
	Class EffectClass
	Scope string // external scope name; "" for PURE
}

// TriggerKind enumerates explicit authoritative mid-run events.
type TriggerKind string

const (
	TrigDepInvalidated  TriggerKind = "DEPENDENCY_INVALIDATED"
	TrigArtifactArrived TriggerKind = "ARTIFACT_BECAME_VALID"
	TrigArtifactRevoked TriggerKind = "ARTIFACT_REVOKED"
	TrigVerifyFailed    TriggerKind = "VERIFICATION_FAILED"
	TrigCostChanged     TriggerKind = "EXECUTION_COST_CHANGED"
	TrigOpFailed        TriggerKind = "OPERATION_FAILED"
	TrigBudgetReduced   TriggerKind = "BUDGET_REDUCED"
	TrigEffectUnknown   TriggerKind = "EFFECT_UNKNOWN"
	TrigExecutorDown    TriggerKind = "EXECUTOR_DOWN"
)

// Event is one authoritative mid-run occurrence (deterministic, clock-free).
type Event struct {
	ID   string // dedup key; repeated IDs never re-switch
	Kind TriggerKind
	// Payloads (by kind):
	Node      string // affected node id (dep/cost/verify/op/budget scope)
	InputName string // DEP_INVALIDATED: which input ("" = op-version bump)
	NewDigest string // DEP_INVALIDATED: new digest value
	NewCost   int    // COST_CHANGED: new CostUnits (-1 = missing)
	JobID     string // ARTIFACT_REVOKED: outcome job to revoke
	Op        string // OP_FAILED / EXECUTOR_DOWN / VERIFY_FAILED: op label
	Scope     string // EFFECT_UNKNOWN: external scope left ambiguous
	Budget    int    // BUDGET_REDUCED: new total budget units
	Arrival   *ArrivalPayload
}

// ArrivalPayload carries an independently produced VALID artifact.
type ArrivalPayload struct {
	Key        reuse.ExecutionKey
	Bytes      []byte
	EvidenceID string
	CostUSD    float64
	ReceiptID  string
	JobID      string
}

// ScheduledTrigger fires Event after node AfterNode is visited (any
// resolution). Empty AfterNode fires before the run starts.
type ScheduledTrigger struct {
	AfterNode string
	Event     Event
}

// EffectRecord is one committed external effect (history, immutable).
type EffectRecord struct {
	Scope  string
	Digest string // digest of the computation key that produced it
	Key    string // execution-key digest
	Status string // "committed" | "unknown"
	NodeID string
}

// SwitchRecord audits one plan switch.
type SwitchRecord struct {
	Seq        int
	TriggerID  string
	Trigger    TriggerKind
	OldPlan    string
	NewPlan    string
	Quotes     map[string]int // legal candidate -> remaining units
	Rejected   map[string]string
	SpentUnits int
}

// NoLegalSuffix is returned when triggers leave no executable remaining plan.
// It is a first-class negative result, never a panic.
type NoLegalSuffix struct {
	TriggerID string
	Reason    string
	Spent     int
}

func (e *NoLegalSuffix) Error() string {
	return fmt.Sprintf("replan: no legal suffix after %s: %s (spent %d)",
		e.TriggerID, e.Reason, e.Spent)
}

// ExecutionState is the durable per-run boundary state (contract Phase 1).
type ExecutionState struct {
	Completed map[string]plan.NodeOutcome
	Effects   map[string]EffectRecord
	Switches  []SwitchRecord
	Spent     int
	Budget    int // -1 = unlimited
}

var _ = fmt.Sprint
