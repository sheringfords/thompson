package materialization

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// cache.go: durable conventional exact cache (modes B/B+). Append-only JSONL,
// fsync per new entry, in-memory index, deterministic replay — same durability
// discipline as the artifact store, so wall-clock comparisons isolate
// semantics (graph/evidence), not persistence. B ignores evidence; B+ binds
// verifier contract + outcome status per entry (still no graph, no traversal).

// CacheEntry is one cached computation.
type CacheEntry struct {
	Key       string `json:"key"`
	BytesHex  string `json:"bytes_hex"`
	OutDigest string `json:"out_digest"`
	Contract  string `json:"contract"`
	JobID     string `json:"job_id"`
	Version   uint64 `json:"version"`
	Status    string `json:"status"` // outcome status at write
}

// FileCache is a durable exact-match cache.
type FileCache struct {
	mu   sync.Mutex
	file *os.File
	idx  map[string]CacheEntry
}

// OpenCache creates or opens the cache, replaying history.
func OpenCache(path string) (*FileCache, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	c := &FileCache{file: f, idx: map[string]CacheEntry{}}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e CacheEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("cache: corrupt entry: %w", err)
		}
		c.idx[e.Key] = e
	}
	if err := sc.Err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 2); err != nil {
		_ = f.Close()
		return nil, err
	}
	return c, nil
}

// Lookup returns the entry for an exact key match.
func (c *FileCache) Lookup(key string) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.idx[key]
	return e, ok
}

// Store appends one entry (overwrite semantics via replay order).
func (c *FileCache) Store(e CacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := c.file.Write(line); err != nil {
		return err
	}
	if err := c.file.Sync(); err != nil {
		return err
	}
	c.idx[e.Key] = e
	return nil
}

// Close closes the cache file.
func (c *FileCache) Close() error { return c.file.Close() }

// Raw returns entry bytes.
func (e CacheEntry) Raw() []byte { return []byte(e.BytesHex) }

// CacheKey builds the conventional key: op + input digests + config digests.
// Deliberately NO verifier contract, executor, outcome or graph position:
// that absence is the documented evidence gap under test, not a strawman
// (conventional caches key by computational inputs).
func CacheKey(op string, inputs ...string) string {
	h := "op:" + op + "|"
	for _, in := range inputs {
		h += fmt.Sprintf("%d:%s|", len(in), in)
	}
	return reuse.DigestString(h)
}

func hexBytes(b []byte) string { return string(b) }
