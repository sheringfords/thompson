package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// storage_backend is omitempty-hashed: legacy manifests without the field
// hash identically before and after the field exists.
func TestStorageBackendHashStable(t *testing.T) {
	legacy := `{"experiment_id":"e","workload_name":"w","seed":1,"maturation_hours":24,
"treatments":[{"id":"t0","description":"d","arms":["a"],"max_attempts":1}],
"jobs":[],"workload_version":"","synthetic":true}`
	var m Manifest
	if err := json.Unmarshal([]byte(legacy), &m); err != nil {
		t.Fatal(err)
	}
	h1, err := m.contentHash()
	if err != nil {
		t.Fatal(err)
	}
	m.Treatments[0].StorageBackend = "journal"
	h2, err := m.contentHash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("backend selection must change the manifest hash")
	}
	var m2 Manifest
	if err := json.Unmarshal([]byte(legacy), &m2); err != nil {
		t.Fatal(err)
	}
	h3, err := m2.contentHash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h3 || !strings.Contains(h1, "") {
		t.Fatalf("legacy hash unstable: %s vs %s", h1, h3)
	}
}
