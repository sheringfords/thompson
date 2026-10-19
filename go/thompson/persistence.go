package thompson

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
)

// SnapshotStore persists policy snapshots durably.
type SnapshotStore interface {
	Save(snapshot Snapshot) error
	Load() (*Snapshot, error)
}

// MemoryStore is an in-memory SnapshotStore for tests and harness use.
// Mutex-guarded: Policy + gateway use it concurrently (parity with Rust
// `MemoryStore` RwLock in `crates/thompson-sampling/src/persistence.rs:24`).
type MemoryStore struct {
	mu   sync.RWMutex
	data []byte
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (m *MemoryStore) Save(s Snapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = b
	return nil
}

func (m *MemoryStore) Load() (*Snapshot, error) {
	m.mu.RLock()
	data := m.data
	m.mu.RUnlock()
	if data == nil {
		return nil, nil
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// FileStore persists snapshots atomically via write-then-rename.
type FileStore struct {
	Path string
}

func NewFileStore(path string) *FileStore { return &FileStore{Path: path} }

func (f *FileStore) Save(s Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 10*1024*1024 {
		return fmt.Errorf("thompson: snapshot too large (%d bytes)", len(b))
	}
	// pid + random suffix: fixed `.tmp` collides when two processes/savers
	// flush the same path concurrently (Rust uses `tmp.<pid>` in
	// `crates/thompson-sampling/src/persistence.rs:75` for the same reason).
	tmp := fmt.Sprintf("%s.tmp.%d.%d", f.Path, os.Getpid(), rand.IntN(1<<30))
	// Use non-cached write with fsync for durability
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := file.Write(b); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return err
	}
	file.Close()
	if err := os.Rename(tmp, f.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	// Best-effort fsync parent directory
	if dir, err := os.Open(filepath.Dir(f.Path)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

func (f *FileStore) Load() (*Snapshot, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(b) > 10*1024*1024 {
		return nil, fmt.Errorf("thompson: snapshot too large (%d bytes)", len(b))
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
