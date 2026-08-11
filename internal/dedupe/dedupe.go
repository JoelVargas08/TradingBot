package dedupe

import "sync"

type Deduplicator struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	limit int
}

func New(limit int) *Deduplicator {
	return &Deduplicator{
		seen:  make(map[string]struct{}),
		limit: limit,
	}
}

func (d *Deduplicator) Seen(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return true
	}
	if len(d.seen) >= d.limit {
		d.seen = make(map[string]struct{})
	}
	d.seen[key] = struct{}{}
	return false
}
