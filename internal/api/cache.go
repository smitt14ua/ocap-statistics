package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/report"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// response is a rendered body with its content type and, for larger bodies, a gzipped copy.
type response struct {
	contentType string
	body        []byte
	gzipped     []byte
	filename    string
}

// minGzipSize is the smallest body worth compressing.
const minGzipSize = 1024

func (r *response) compress() {
	if len(r.body) < minGzipSize {
		return
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	_, _ = zw.Write(r.body)
	if zw.Close() == nil && buf.Len() < len(r.body) {
		r.gzipped = buf.Bytes()
	}
}

func (r *response) size() int { return len(r.body) + len(r.gzipped) }

// responseCache keeps rendered responses until the statistics change (generation) or the
// cache grows past its limits, at which point it starts over.
type responseCache struct {
	mu       sync.Mutex
	gen      int64
	items    map[string]response
	bytes    int
	maxItems int
	maxBytes int
}

func newResponseCache(maxItems, maxBytes int) *responseCache {
	return &responseCache{items: map[string]response{}, maxItems: maxItems, maxBytes: maxBytes}
}

func (c *responseCache) get(gen int64, key string) (response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return response{}, false
	}
	r, ok := c.items[key]
	return r, ok
}

func (c *responseCache) put(gen int64, key string, r response) {
	if r.size() > c.maxBytes/4 {
		return // never let one response evict everything
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen || len(c.items) >= c.maxItems || c.bytes+r.size() > c.maxBytes {
		c.gen = gen
		clear(c.items)
		c.bytes = 0
	}
	if old, ok := c.items[key]; ok {
		c.bytes -= old.size()
	}
	c.items[key] = r
	c.bytes += r.size()
}

// cacheParams are the query parameters that affect statistics responses. Other parameters
// are ignored, so they cannot be used to bypass or flood the cache.
var cacheParams = []string{"from", "to", "tag", "min_duration", "search", "squad", "sort", "order", "limit", "offset", "format"}

func cacheKey(path string, q url.Values) string {
	var b strings.Builder
	b.WriteString(path)
	for _, k := range cacheParams {
		vs := q[k]
		if len(vs) == 0 {
			continue
		}
		vs = slices.Clone(vs)
		slices.Sort(vs)
		for _, v := range vs {
			b.WriteString("&" + k + "=" + url.QueryEscape(v))
		}
	}
	return b.String()
}

// built is the aggregation of one filter, shared by all endpoints.
type built struct {
	res     *report.Result
	longest *store.Kill
	ids     *report.Identities
	names   *report.Renamer // players of the filter's missions
}

// resultCache caches aggregations per filter. Concurrent requests for the same filter share
// one computation, so a burst of requests costs one database scan.
type resultCache struct {
	mu    sync.Mutex
	gen   int64
	items map[store.Filter]*resultEntry
	limit int
}

type resultEntry struct {
	done chan struct{}
	val  built
	err  error
}

func newResultCache(limit int) *resultCache {
	return &resultCache{items: map[store.Filter]*resultEntry{}, limit: limit}
}

// computeTimeout bounds one aggregation; it runs detached from the request that started it.
const computeTimeout = time.Minute

func (c *resultCache) get(ctx context.Context, gen int64, f store.Filter, compute func(context.Context) (built, error)) (built, error) {
	c.mu.Lock()
	if gen != c.gen {
		c.gen = gen
		clear(c.items)
	}
	e, ok := c.items[f]
	if !ok {
		if len(c.items) >= c.limit {
			clear(c.items)
		}
		e = &resultEntry{done: make(chan struct{})}
		c.items[f] = e
		c.mu.Unlock()

		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computeTimeout)
		e.val, e.err = compute(cctx)
		cancel()
		close(e.done)
		if e.err != nil {
			c.mu.Lock()
			if c.items[f] == e {
				delete(c.items, f)
			}
			c.mu.Unlock()
		}
		return e.val, e.err
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e.val, e.err
	case <-ctx.Done():
		return built{}, ctx.Err()
	}
}
