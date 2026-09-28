package journal

import (
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Decision is the journal's committed-selection record. It mirrors the
// fields of the production committed decision that the benchmark compares;
// policy-identity, rule version, config digest, and fallback flag travel
// with every row. The score_kind/eligible_state/cost_per_success fields
// are gateway-evidence fidelity additions (omitempty): old rows without
// them replay identically.
type Decision struct {
	DecisionID  string             `json:"decision_id"`
	JobID       string             `json:"job_id"`
	StrategyID  string             `json:"strategy_id"`
	SelectedArm string             `json:"selected_arm"`
	Eligible    []string           `json:"eligible"`
	Scores      map[string]float64 `json:"scores,omitempty"`
	PolicyID    string             `json:"policy_id"`
	RuleVersion int                `json:"rule_version"`
	Objective   string             `json:"objective,omitempty"`
	ConfigHash  string             `json:"config_hash"`
	Fallback    bool               `json:"fallback"`
	Explore     bool               `json:"explore"`
	// Gateway fidelity (omitempty, default zero values).
	ScoreKind     string             `json:"score_kind,omitempty"`
	EligibleState []ArmState         `json:"eligible_state,omitempty"`
	CostPerSuc    map[string]float64 `json:"cost_per_success,omitempty"`
}

// ArmState is one eligible arm's posterior snapshot at decision time.
type ArmState struct {
	ArmID string  `json:"arm_id"`
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	Pulls uint64  `json:"pulls"`
}

// OutcomeAttempt is one execution attempt inside a settled outcome.
// Fields below Verified are gateway-fidelity additions (omitempty;
// assay-only rows omit them and replay identically).
type OutcomeAttempt struct {
	AttemptID string   `json:"attempt_id"`
	ArmID     string   `json:"arm_id"`
	CostUSD   *float64 `json:"cost_usd,omitempty"`
	Verified  string   `json:"verified"`
	ExecutorID       string            `json:"executor_id,omitempty"`
	Transport        string            `json:"transport,omitempty"`
	LatencyMs        float64           `json:"latency_ms,omitempty"`
	Validation       string            `json:"validation,omitempty"`
	VerifiedBy       string            `json:"verified_by,omitempty"`
	VerifiedAt       string            `json:"verified_at,omitempty"`
	InputTokens      *int              `json:"input_tokens,omitempty"`
	OutputTokens     *int              `json:"output_tokens,omitempty"`
	FailureCategory  string            `json:"failure_category,omitempty"`
	FieldCorrections []FieldCorrection `json:"field_corrections,omitempty"`
}

// FieldCorrection records one validator/human field-level fix (diagnostic).
type FieldCorrection struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// SettledOutcome is the journal's versioned outcome record. Timestamp and
// schema fields are gateway-fidelity additions (omitempty).
type SettledOutcome struct {
	SchemaVersion   int              `json:"schema_version,omitempty"`
	EventType       string           `json:"event_type,omitempty"`
	DecisionID      string           `json:"decision_id"`
	JobID           string           `json:"job_id"`
	Version         uint64           `json:"version"`
	Supersedes      uint64           `json:"supersedes"`
	Status          string           `json:"status"`
	Attempts        []OutcomeAttempt `json:"attempts"`
	DecidingAttempt string           `json:"deciding_attempt"`
	HumanReviewCost *float64         `json:"human_review_cost_usd,omitempty"`
	VerifiedBy      string           `json:"verified_by,omitempty"`
	VerifiedAt      string           `json:"verified_at,omitempty"`
	CorrectedAt     string           `json:"corrected_at,omitempty"`
	OccurredAt      string           `json:"occurred_at,omitempty"`
}

// SafetyTransition is a versioned operator/monitor safety action.
type SafetyTransition struct {
	Actor    string `json:"actor"`
	Type     string `json:"type"`
	Arm      string `json:"arm,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Nonce    string `json:"nonce"`
}

// Assignment binds a job to a treatment before execution.
type Assignment struct {
	JobID       string  `json:"job_id"`
	Strata      string  `json:"strata"`
	Treatment   string  `json:"treatment"`
	Probability float64 `json:"probability"`
}

// CommitDecision durably commits one selection AND its exploration-budget
// consumption in a single transaction: the two can never disagree after
// recovery (the W6 partial-commit state is unrepresentable). Duplicate
// decision IDs return the existing row (committed=false); conflicting
// reuse is rejected.
func (j *Journal) CommitDecision(d Decision, cfgDigest string) (seq uint64, committed bool, err error) {
	if d.DecisionID == "" || d.JobID == "" || len(d.Eligible) == 0 {
		return 0, false, fmt.Errorf("journal: decision_id, job_id and eligible arms are required")
	}
	in := false
	for _, a := range d.Eligible {
		if a == d.SelectedArm {
			in = true
			break
		}
	}
	if !in {
		return 0, false, fmt.Errorf("journal: selected arm %q not eligible", d.SelectedArm)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return 0, false, err
	}
	tx, err := j.db.Begin()
	if err != nil {
		return 0, false, busyErr(err)
	}
	defer tx.Rollback()
	var prev string
	err = tx.QueryRow(`SELECT payload FROM events WHERE kind='decision' AND event_key=?`, d.DecisionID).Scan(&prev)
	switch err {
	case nil:
		var old Decision
		if uerr := json.Unmarshal([]byte(prev), &old); uerr != nil {
			return 0, false, uerr
		}
		if old.SelectedArm != d.SelectedArm || old.JobID != d.JobID {
			return 0, false, fmt.Errorf("journal: conflicting reuse of decision %q", d.DecisionID)
		}
		return 0, false, nil // idempotent redelivery
	case sql.ErrNoRows:
	default:
		return 0, false, busyErr(err)
	}
	res, err := tx.Exec(`INSERT INTO events(kind, job_id, version, event_key, payload, config_digest, created_ns) VALUES('decision',?,0,?,?,?,?)`,
		d.JobID, d.DecisionID, string(raw), cfgDigest, nowNS())
	if err != nil {
		return 0, false, busyErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	if d.Explore {
		if _, err := tx.Exec(`INSERT INTO exploration(arm, used) VALUES(?,1) ON CONFLICT(arm) DO UPDATE SET used=used+1`, d.SelectedArm); err != nil {
			return 0, false, busyErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, busyErr(err)
	}
	return uint64(id), true, nil
}

// SettleOutcome commits one versioned outcome atomically: structural
// validation, attribution against the committed decision, version rules
// (duplicate idempotent, stale rejected, gap rejected), and malformed-cost
// refusal all happen inside the same transaction as the insert.
func (j *Journal) SettleOutcome(o SettledOutcome, cfgDigest string) (applied bool, err error) {
	if err := validateOutcome(o); err != nil {
		return false, err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return false, err
	}
	tx, err := j.db.Begin()
	if err != nil {
		return false, busyErr(err)
	}
	defer tx.Rollback()
	// Attribution: single-attempt outcomes must name the selected arm;
	// multi-attempt outcomes must stay within the eligible set.
	var decRaw string
	err = tx.QueryRow(`SELECT payload FROM events WHERE kind='decision' AND event_key=?`, o.DecisionID).Scan(&decRaw)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("journal: unknown decision %q", o.DecisionID)
	}
	if err != nil {
		return false, busyErr(err)
	}
	var dec Decision
	if err := json.Unmarshal([]byte(decRaw), &dec); err != nil {
		return false, err
	}
	if dec.JobID != o.JobID {
		return false, fmt.Errorf("journal: job mismatch %q vs %q", o.JobID, dec.JobID)
	}
	if err := checkOutcomeAttribution(dec, o); err != nil {
		return false, err
	}
	key := fmt.Sprintf("%s#%d", o.JobID, o.Version)
	var prev string
	err = tx.QueryRow(`SELECT payload FROM events WHERE kind='outcome' AND event_key=?`, key).Scan(&prev)
	if err == nil {
		return false, nil // duplicate delivery: idempotent
	}
	if err != sql.ErrNoRows {
		return false, busyErr(err)
	}
	if o.Version > 1 {
		var have int
		err = tx.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='outcome' AND event_key=?`, fmt.Sprintf("%s#%d", o.JobID, o.Version-1)).Scan(&have)
		if err != nil {
			return false, busyErr(err)
		}
		if have == 0 {
			// Allow first-version-must-be-1 and version gaps to refuse, but a
			// v2 arriving before v1 is visible is a gap: refuse loudly.
			return false, fmt.Errorf("journal: version gap for job %q at v%d", o.JobID, o.Version)
		}
	} else if o.Version != 1 {
		return false, fmt.Errorf("journal: first version for job %q must be 1", o.JobID)
	}
	// Stale check: a newer version already settled wins over redelivered old.
	var newer int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='outcome' AND job_id=? AND version>?`, o.JobID, o.Version).Scan(&newer); err != nil {
		return false, busyErr(err)
	}
	if newer > 0 {
		return false, nil // stale redelivery: acknowledged, learns nothing
	}
	if _, err := tx.Exec(`INSERT INTO events(kind, job_id, version, event_key, payload, config_digest, created_ns) VALUES('outcome',?,?,?,?,?,?)`,
		o.JobID, o.Version, key, string(raw), cfgDigest, nowNS()); err != nil {
		return false, busyErr(err)
	}
	if err := tx.Commit(); err != nil {
		return false, busyErr(err)
	}
	return true, nil
}

// RecordSafety persists one safety transition before its effect is visible:
// callers apply the effect only after this returns nil. An empty Nonce
// disables idempotency (pure append, matching the gateway safety log);
// a non-empty Nonce dedups operator retries.
func (j *Journal) RecordSafety(s SafetyTransition, cfgDigest string) (seq uint64, applied bool, err error) {
	if s.Actor == "" || s.Type == "" {
		return 0, false, fmt.Errorf("journal: safety actor and type are required")
	}
	switch s.Type {
	case "ARM_SUSPENDED", "ARM_RESUMED", "ARM_DISABLED", "EMERGENCY_STOP", "EMERGENCY_RELEASED", "APPROVE":
	default:
		return 0, false, fmt.Errorf("journal: unknown safety type %q", s.Type)
	}
	if s.Nonce == "" {
		// No idempotency key: mint a unique one so the append-only row
		// never collides on the (kind, event_key) constraint.
		var b [16]byte
		if _, err := cryptorand.Read(b[:]); err != nil {
			return 0, false, fmt.Errorf("journal: nonce: %w", err)
		}
		s.Nonce = fmt.Sprintf("%x", b[:])
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return 0, false, err
	}
	tx, err := j.db.Begin()
	if err != nil {
		return 0, false, busyErr(err)
	}
	defer tx.Rollback()
	if s.Nonce != "" {
		var prev string
		err = tx.QueryRow(`SELECT payload FROM events WHERE kind='safety' AND event_key=?`, s.Nonce).Scan(&prev)
		if err == nil {
			return 0, false, nil // idempotent operator retry
		}
		if err != sql.ErrNoRows {
			return 0, false, busyErr(err)
		}
	}
	res, err := tx.Exec(`INSERT INTO events(kind, job_id, version, event_key, payload, config_digest, created_ns) VALUES('safety','',0,?,?,?,?)`,
		s.Nonce, string(raw), cfgDigest, nowNS())
	if err != nil {
		return 0, false, busyErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, busyErr(err)
	}
	return uint64(id), true, nil
}

// RecordAssignment persists one assignment row; repeats are idempotent.
func (j *Journal) RecordAssignment(a Assignment) error {
	if a.JobID == "" || a.Treatment == "" {
		return fmt.Errorf("journal: job and treatment are required")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`INSERT INTO events(kind, job_id, version, event_key, payload, config_digest, created_ns) VALUES('assignment',?,0,?,?,?,?) ON CONFLICT(kind, event_key) DO NOTHING`,
		a.JobID, a.Treatment+"\x00"+a.JobID, string(raw), "", nowNS())
	return busyErr(err)
}

func busyErr(err error) error {
	if err == nil {
		return nil
	}
	if isBusy(err) {
		return fmt.Errorf("journal: database is locked by another live writer (single-writer enforced): %w", err)
	}
	return err
}
