// Package journal implements the transactional-journal assay prototype: one
// SQLite WAL database carrying every authoritative execution fact in a
// single commit sequence, with all policy/cost/safety/monitor state as
// rebuildable projections. Research prototype only: it never touches the
// production gateway or its ledgers.
package journal

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion versions the journal schema. Open refuses anything else.
const SchemaVersion = 1

// Event kinds stored in the single events table.
const (
	KindAssignment = "assignment"
	KindDecision   = "decision"
	KindOutcome    = "outcome"
	KindSafety     = "safety"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	job_id TEXT NOT NULL DEFAULT '',
	version INTEGER NOT NULL DEFAULT 0,
	event_key TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL,
	config_digest TEXT NOT NULL DEFAULT '',
	created_ns INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_key ON events(kind, event_key);
CREATE INDEX IF NOT EXISTS idx_events_job ON events(job_id, version);
CREATE TABLE IF NOT EXISTS exploration (
	arm TEXT PRIMARY KEY,
	used INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS checkpoints (
	name TEXT PRIMARY KEY,
	state TEXT NOT NULL,
	ledger_seq INTEGER NOT NULL
);
`

// Journal is one transactional execution journal. Single writer, enforced
// by SQLite itself: busy_timeout(0) turns every lock contention into an
// immediate SQLITE_BUSY error (fail-closed, no queueing). This differs
// deliberately from the flock ledgers, which refuse the second OPEN:
// SQLite cannot express open-time refusal (connections are cheap and
// lock-free until first contended write), so the contract boundary moves
// from open to first contended transaction. journal_test.go proves a
// contender holding a write transaction makes every other writer fail
// loudly instead of interleaving history.
type Journal struct {
	db   *sql.DB
	path string
}

// Open creates or opens the journal at path, enforcing WAL + FULL
// synchronous durability, schema validation, and the writer contract.
func Open(path string) (*Journal, error) {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, fmt.Errorf("journal: mkdir: %w", err)
	}
	// Private cache (default): each handle locks independently, so lock
	// contention between writers surfaces as SQLITE_BUSY instead of
	// serializing inside a shared cache.
	db, err := sql.Open("sqlite", "file:"+path)
	// Single connection process-wide: transactions serialize here instead
	// of contending inside the driver. Cross-PROCESS contention still
	// surfaces as SQLITE_BUSY (fail-closed); in-process callers never see
	// spurious BUSY from pool interleaving. Matches the single-writer
	// contract in the strong sense.
	db.SetMaxOpenConns(1)
	if err != nil {
		return nil, fmt.Errorf("journal: open: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA busy_timeout=0",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("journal: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("journal: schema: %w", err)
	}
	j := &Journal{db: db, path: path}
	if err := j.checkVersion(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) checkVersion() error {
	var v string
	err := j.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		_, err := j.db.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)`, fmt.Sprint(SchemaVersion))
		return err
	}
	if err != nil {
		return fmt.Errorf("journal: version read: %w", err)
	}
	if v != fmt.Sprint(SchemaVersion) {
		return fmt.Errorf("journal: schema version %s incompatible with %d (refusing: no automatic migration)", v, SchemaVersion)
	}
	return nil
}

// Close closes the database (checkpointing WAL content back on last close).
func (j *Journal) Close() error {
	return j.db.Close()
}

// nowNS is injectable for deterministic tests (payload timestamps only;
// ordering comes from seq, never from clocks).
var nowNS = func() int64 { return time.Now().UnixNano() }

// LatestVersion returns the highest settled version for a job (false when
// none). Adapters use it to distinguish duplicate redelivery (same
// version, idempotent) from stale redelivery (older version, refused)
// without reimplementing version rules.
func (j *Journal) LatestVersion(jobID string) (uint64, bool, error) {
	var v uint64
	err := j.db.QueryRow(`SELECT MAX(version) FROM events WHERE kind='outcome' AND job_id=?`, jobID).Scan(&v)
	if err != nil {
		return 0, false, err
	}
	var n int
	if err := j.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='outcome' AND job_id=?`, jobID).Scan(&n); err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	return v, true, nil
}

// Len returns the committed event count (single sequence length).
func (j *Journal) Len() (int, error) {
	var n int
	if err := j.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind != '__probe'`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// EventsSince returns canonical payloads in commit order after seq.
func (j *Journal) EventsSince(seq uint64) ([]StoredEvent, error) {
	rows, err := j.db.Query(`SELECT seq, kind, job_id, version, event_key, payload, config_digest, created_ns FROM events WHERE seq > ? AND kind != '__probe' ORDER BY seq`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredEvent
	for rows.Next() {
		var e StoredEvent
		if err := rows.Scan(&e.Seq, &e.Kind, &e.JobID, &e.Version, &e.Key, &e.Payload, &e.ConfigDigest, &e.CreatedNS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// StoredEvent is one committed journal row.
type StoredEvent struct {
	Seq          uint64
	Kind         string
	JobID        string
	Version      uint64
	Key          string
	Payload      string
	ConfigDigest string
	CreatedNS    int64
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") ||
		strings.Contains(s, "database is locked") ||
		strings.Contains(s, "database table is locked")
}
