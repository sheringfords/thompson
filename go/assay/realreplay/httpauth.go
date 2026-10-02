package realreplay

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// HTTPAuthority is the local versioned HTTP fixture. It is the sole
// authority for its resources: every representation carries a strong ETag
// (a content hash), every served GET is logged with the ETag served, and
// mutation is conditional.
//
// Conditional commit: PUT requires If-Match on the target. The fixture also
// honors X-If-Match-Resources ("path=etag, path=etag"), checked atomically
// with the target precondition under one lock. This models an authority
// that supports multi-resource preconditions; plain HTTP servers generally
// only offer If-Match on the target (recorded as a limitation).
type HTTPAuthority struct {
	mu   sync.Mutex
	res  map[string]string
	log  []ServedRead
	srv  *http.Server
	ln   net.Listener
	Addr string
	// afterServe, if set, runs under the lock after a GET is served and
	// may return writes to apply (used to inject a change mid-replay).
	afterServe func(path string) map[string]string
}

// SetAfterServe installs (or clears, with nil) the mid-run change hook.
func (h *HTTPAuthority) SetAfterServe(f func(path string) map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterServe = f
}

// ServedRead is one logged GET: the authority's own record of which
// version it served and when.
type ServedRead struct {
	Path string `json:"path"`
	ETag string `json:"etag"`
	AtMS int64  `json:"at_ms"`
}

// ETagOf is the strong ETag of a representation.
func ETagOf(body string) string {
	h := sha256.Sum256([]byte(body))
	return `"` + hex.EncodeToString(h[:8]) + `"`
}

// NewHTTPAuthority starts the fixture on addr (e.g. "127.0.0.1:18741").
func NewHTTPAuthority(addr string, resources map[string]string) (*HTTPAuthority, error) {
	h := &HTTPAuthority{res: map[string]string{}}
	for k, v := range resources {
		h.res[k] = v
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	h.ln = ln
	h.Addr = ln.Addr().String()
	h.srv = &http.Server{Handler: http.HandlerFunc(h.serve)}
	go h.srv.Serve(ln)
	return h, nil
}

// Close stops the fixture.
func (h *HTTPAuthority) Close() { h.srv.Close() }

// Reset replaces all resource state and clears the served log.
func (h *HTTPAuthority) Reset(resources map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.res = map[string]string{}
	for k, v := range resources {
		h.res[k] = v
	}
	h.log = nil
}

// Set is an out-of-band concurrent writer (the injected state change).
func (h *HTTPAuthority) Set(path, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.res[path] = body
}

// Current returns the current representation and its ETag.
func (h *HTTPAuthority) Current(path string) (string, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.res[path]
	if !ok {
		return "", "absent", false
	}
	return b, ETagOf(b), true
}

// Log returns a copy of the served-read log.
func (h *HTTPAuthority) Log() []ServedRead {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ServedRead(nil), h.log...)
}

// Paths returns all resource paths, sorted.
func (h *HTTPAuthority) Paths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ps []string
	for p := range h.res {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

func (h *HTTPAuthority) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := r.URL.Path
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		body, ok := h.res[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		et := ETagOf(body)
		h.log = append(h.log, ServedRead{Path: path, ETag: et, AtMS: time.Now().UnixMilli()})
		w.Header().Set("ETag", et)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
		}
		if h.afterServe != nil {
			for p, b := range h.afterServe(path) {
				h.res[p] = b
			}
		}
	case http.MethodPut:
		cur, ok := h.res[path]
		curTag := "absent"
		if ok {
			curTag = ETagOf(cur)
		}
		if im := r.Header.Get("If-Match"); im == "" || im != curTag {
			http.Error(w, "precondition failed: target", http.StatusPreconditionFailed)
			return
		}
		for _, kv := range splitPre(r.Header.Get("X-If-Match-Resources")) {
			b, ok := h.res[kv[0]]
			tag := "absent"
			if ok {
				tag = ETagOf(b)
			}
			if tag != kv[1] {
				http.Error(w, "precondition failed: "+kv[0], http.StatusPreconditionFailed)
				return
			}
		}
		b, _ := io.ReadAll(r.Body)
		h.res[path] = string(b)
		w.Header().Set("ETag", ETagOf(string(b)))
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// splitPre parses `path=etag, path=etag` (etags are quoted, no commas).
func splitPre(s string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.Index(part, "=")
		if i <= 0 {
			continue
		}
		out = append(out, [2]string{part[:i], part[i+1:]})
	}
	return out
}

// ConditionalPut commits body to target only if target is still at
// targetTag and every premise resource still has its witnessed ETag.
func ConditionalPut(base, target, targetTag, body string, premises map[string]string) (int, error) {
	req, err := http.NewRequest(http.MethodPut, "http://"+base+target, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("If-Match", targetTag)
	var parts []string
	for p, t := range premises {
		parts = append(parts, p+"="+t)
	}
	sort.Strings(parts)
	if len(parts) > 0 {
		req.Header.Set("X-If-Match-Resources", strings.Join(parts, ", "))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// State returns a copy of all resource representations.
func (h *HTTPAuthority) State() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := map[string]string{}
	for k, v := range h.res {
		m[k] = v
	}
	return m
}
