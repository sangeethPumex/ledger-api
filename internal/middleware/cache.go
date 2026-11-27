package middleware

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxEntries = 100 // custom date ranges make new keys; past this the cache starts over

// Cache keeps the successful replies of a few slow GET routes in memory for a short while. One Cache serves one person:
// wrap it inside the API key check. Any write passing through it empties it.
type Cache struct {
	ttl   time.Duration
	paths map[string]bool

	mu    sync.Mutex
	gen   uint64 // bumped by Clear, so a reply computed across a write is not kept
	items map[string]reply
}

type reply struct {
	at          time.Time
	contentType string
	body        []byte
}

func NewCache(ttl time.Duration, paths ...string) *Cache {
	c := &Cache{ttl: ttl, paths: map[string]bool{}, items: map[string]reply{}}
	for _, p := range paths {
		c.paths[p] = true
	}
	return c
}

// Clear drops every kept reply.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.items)
}

func (c *Cache) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The list searches are POSTs that only read.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasSuffix(r.URL.Path, "/search") {
			c.Clear()
			defer c.Clear()
		}
		if r.Method != http.MethodGet || !c.paths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		key := r.URL.Path + "?" + r.URL.RawQuery
		c.mu.Lock()
		hit, ok := c.items[key]
		gen := c.gen
		c.mu.Unlock()
		if ok && time.Since(hit.at) < c.ttl {
			w.Header().Set("Content-Type", hit.contentType)
			_, _ = w.Write(hit.body)
			return
		}
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusOK || rec.failed || r.Context().Err() != nil {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if gen != c.gen {
			return
		}
		if len(c.items) >= maxEntries {
			clear(c.items)
		}
		c.items[key] = reply{time.Now(), w.Header().Get("Content-Type"), rec.body.Bytes()}
	})
}

// recorder passes a reply through while keeping a copy of it.
type recorder struct {
	http.ResponseWriter
	status int
	failed bool // a write to the client failed, so the copy may be cut short
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	n, err := r.ResponseWriter.Write(b)
	r.failed = r.failed || err != nil
	return n, err
}
