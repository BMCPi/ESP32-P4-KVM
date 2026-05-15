//go:build tinygo

package main

import (
	"sync"
	"time"
)

// PSRAMCacheConfig is sized for PSRAM: 32 entries × 128 KiB = 4 MiB total.
// Use this after enabling PSRAM in the linker script (see esp32p4-psram.ld).
var PSRAMCacheConfig = CacheConfig{ //nolint:gochecknoglobals // intentional package-level default
	Capacity: 32,
	SlotSize: 128 * 1024,
}

// SRAMCacheConfig is sized for internal SRAM: 8 entries × 4 KiB = 32 KiB.
// Use this when the heap lives in SRAM (the default linker script).
var SRAMCacheConfig = CacheConfig{ //nolint:gochecknoglobals // intentional package-level default
	Capacity: 8,
	SlotSize: 4 * 1024,
}

// CacheConfig controls the memory layout of a MemCache.
type CacheConfig struct {
	// Capacity is the maximum number of concurrent cache entries.
	Capacity int
	// SlotSize is the maximum value size per entry in bytes.
	// Total arena = Capacity × SlotSize.
	SlotSize int
}

// cacheSlot is one entry in the cache table.
type cacheSlot struct {
	key     string
	size    int       // actual bytes written to this slot's arena region
	expiry  time.Time // zero value means no expiry
	lruPrev int       // index of the more-recently-used neighbour, or lruNone
	lruNext int       // index of the less-recently-used neighbour, or lruNone
	used    bool
}

const lruNone = -1

// MemCache is an LRU key-value cache backed by a contiguous byte arena.
//
// # PSRAM placement
//
// The arena is allocated on the Go heap at construction time.  By default the
// ESP32-P4 heap lives in internal SRAM (~444 KiB).  To move the heap — and
// therefore this arena — into PSRAM:
//
//  1. Copy targets/esp32p4.ld from the TinyGo installation and add a PSRAM
//     memory region (typically origin 0x3C000000, length 16 MiB or 32 MiB).
//  2. Redirect _heap_start/_heap_end to that region.
//  3. Initialize PSRAM hardware before the first heap allocation (call
//     psramInit() or the equivalent ESP-IDF routine at startup).
//  4. Point the target JSON at your modified linker script.
//
// With a PSRAM heap, PSRAMCacheConfig (32 × 128 KiB = 4 MiB) fits easily.
type MemCache struct {
	mu      sync.Mutex
	cfg     CacheConfig
	slots   []cacheSlot
	arena   []byte // len = cfg.Capacity * cfg.SlotSize
	lruHead int    // index of the most-recently-used slot
	lruTail int    // index of the least-recently-used slot (eviction candidate)
	count   int
}

// NewMemCache allocates and returns a MemCache using cfg.
func NewMemCache(cfg CacheConfig) *MemCache {
	c := &MemCache{
		cfg:     cfg,
		slots:   make([]cacheSlot, cfg.Capacity),
		arena:   make([]byte, cfg.Capacity*cfg.SlotSize),
		lruHead: lruNone,
		lruTail: lruNone,
	}
	for i := range c.slots {
		c.slots[i].lruPrev = lruNone
		c.slots[i].lruNext = lruNone
	}

	return c
}

// Set stores value under key with an optional TTL (zero means no expiry).
// Returns false if len(value) > cfg.SlotSize.
func (c *MemCache) Set(key string, value []byte, ttl time.Duration) bool {
	if len(value) > c.cfg.SlotSize {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.evictExpired()

	idx := c.findKey(key)
	isNew := idx == lruNone

	switch {
	case !isNew:
		// Updating an existing entry: pull it out of the LRU chain and
		// re-insert at head below.
		c.removeFromLRU(idx)

	case c.count == c.cfg.Capacity:
		// Cache full: evict the least-recently-used entry.
		idx = c.lruTail
		c.removeFromLRU(idx)
		c.slots[idx].used = false
		c.count--

	default:
		idx = c.findFreeSlot()
	}

	slot := &c.slots[idx]
	slot.key = key
	slot.size = len(value)
	slot.used = true

	if ttl > 0 {
		slot.expiry = time.Now().Add(ttl)
	} else {
		slot.expiry = time.Time{}
	}

	copy(c.arena[idx*c.cfg.SlotSize:], value)
	c.pushToHead(idx)

	if isNew {
		c.count++
	}

	return true
}

// Get returns the value stored under key and whether it was found.
// A found-but-expired entry is deleted and reported as missing.
// The returned slice is a copy; callers may modify it freely.
func (c *MemCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	idx := c.findKey(key)
	if idx == lruNone {
		return nil, false
	}

	slot := &c.slots[idx]
	if !slot.expiry.IsZero() && time.Now().After(slot.expiry) {
		c.evictSlot(idx)

		return nil, false
	}

	// Move to head (most-recently used).
	c.removeFromLRU(idx)
	c.pushToHead(idx)

	out := make([]byte, slot.size)
	copy(out, c.arena[idx*c.cfg.SlotSize:idx*c.cfg.SlotSize+slot.size])

	return out, true
}

// Delete removes the entry for key. Returns true if the key existed.
func (c *MemCache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	idx := c.findKey(key)
	if idx == lruNone {
		return false
	}

	c.evictSlot(idx)

	return true
}

// Len returns the number of live (non-expired) entries.
func (c *MemCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.count
}

// ArenaBytes returns the total size of the backing arena in bytes
// (cfg.Capacity × cfg.SlotSize).
func (c *MemCache) ArenaBytes() int {
	return len(c.arena)
}

// findKey returns the slot index for key, or lruNone if not present.
// O(Capacity) linear scan — acceptable for small caches (≤ 64 entries).
func (c *MemCache) findKey(key string) int {
	for i := range c.slots {
		if c.slots[i].used && c.slots[i].key == key {
			return i
		}
	}

	return lruNone
}

// findFreeSlot returns the first unused slot index.
func (c *MemCache) findFreeSlot() int {
	for i := range c.slots {
		if !c.slots[i].used {
			return i
		}
	}

	return lruNone
}

// evictExpired removes all entries whose TTL has elapsed.
func (c *MemCache) evictExpired() {
	now := time.Now()

	for i := range c.slots {
		if c.slots[i].used && !c.slots[i].expiry.IsZero() && now.After(c.slots[i].expiry) {
			c.evictSlot(i)
		}
	}
}

// evictSlot removes slot idx from the LRU chain and marks it as free.
func (c *MemCache) evictSlot(idx int) {
	c.removeFromLRU(idx)
	c.slots[idx].used = false
	c.slots[idx].key = ""
	c.count--
}

// pushToHead inserts slot idx at the MRU end of the LRU chain.
func (c *MemCache) pushToHead(idx int) {
	c.slots[idx].lruPrev = lruNone
	c.slots[idx].lruNext = c.lruHead

	if c.lruHead != lruNone {
		c.slots[c.lruHead].lruPrev = idx
	}

	c.lruHead = idx

	if c.lruTail == lruNone {
		c.lruTail = idx
	}
}

// removeFromLRU unlinks slot idx from the LRU chain without marking it free.
func (c *MemCache) removeFromLRU(idx int) {
	prev := c.slots[idx].lruPrev
	next := c.slots[idx].lruNext

	if prev != lruNone {
		c.slots[prev].lruNext = next
	} else {
		c.lruHead = next
	}

	if next != lruNone {
		c.slots[next].lruPrev = prev
	} else {
		c.lruTail = prev
	}

	c.slots[idx].lruPrev = lruNone
	c.slots[idx].lruNext = lruNone
}
