package xcodec

import (
	"container/list"
	"crypto/rand"
	"sync"
)

// CacheID identifies a peer cache instance; it is exchanged in the pipe
// protocol <HELLO> message and corresponds to the C++ UUID.
type CacheID [16]byte

// Cache is the segment store used by the encoder and decoder, mirroring
// the C++ XCodecCache interface.  Implementations must be safe for
// concurrent use by multiple sessions.
//
// Segments passed to and returned from a Cache are treated as immutable;
// implementations return copies or shared read-only slices.
type Cache interface {
	// Lookup returns the segment stored for hash, or nil.
	Lookup(hash uint64) []byte

	// Enter stores a new segment.  If the hash is already present the
	// entry is replaced (the C++ code asserts absence; being lenient
	// here keeps streams decodable instead of crashing).
	Enter(hash uint64, segment []byte)

	// Replace overwrites the segment stored for an existing hash,
	// entering it if absent.
	Replace(hash uint64, segment []byte)

	// Touch hints that a segment was used, letting e.g. a secondary
	// cache refresh its eviction state.  The default is a no-op.
	Touch(hash uint64, segment []byte)

	// Has reports whether the cache holds the given hash.
	Has(hash uint64) bool

	// Size returns the number of segments held.
	Size() int

	// OutOfBand reports whether declarations may be exchanged
	// out-of-band.  For out-of-band caches the encoder emits
	// references instead of in-stream <EXTRACT> declarations.
	OutOfBand() bool

	// Connect returns the cache view used to decode a stream whose
	// <HELLO> carried the given peer ID, mirroring the C++
	// XCodecCache::connect behaviour.
	Connect(id CacheID) Cache

	// ID returns this cache's identity, sent in <HELLO>.
	ID() CacheID
}

// memoryEntry is one LRU-tracked segment.
type memoryEntry struct {
	segment []byte
	element *list.Element // nil when unbounded
}

// MemoryCache is the in-memory LRU segment cache, mirroring the C++
// XCodecMemoryCache.  A zero or negative segment limit means unbounded.
type MemoryCache struct {
	mu       sync.Mutex
	id       CacheID
	limit    int
	entries  map[uint64]*memoryEntry
	lru      *list.List // front = most recently used
	children map[CacheID]*MemoryCache
}

// MemoryCacheOption configures a MemoryCache.
type MemoryCacheOption func(*MemoryCache)

// WithSegmentLimit bounds the cache to n segments (LRU eviction).
func WithSegmentLimit(n int) MemoryCacheOption {
	return func(c *MemoryCache) {
		if n > 0 {
			c.limit = n
		}
	}
}

// WithCacheID sets the cache identity exchanged in <HELLO>.
func WithCacheID(id CacheID) MemoryCacheOption {
	return func(c *MemoryCache) { c.id = id }
}

// NewMemoryCache returns an empty memory cache.  Like the C++ version,
// the size limit is expressed in segments (C++ divides the configured
// byte size by SegmentLength).  Unless WithCacheID is given, the cache
// is assigned a fresh random identity, mirroring the C++ activation
// which generates a UUID when the config does not specify one; the
// identity travels in <HELLO> so that peers can share a decoder cache
// across connections to the same cache.
func NewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	c := &MemoryCache{
		entries:  make(map[uint64]*memoryEntry),
		lru:      list.New(),
		children: make(map[CacheID]*MemoryCache),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.id == (CacheID{}) {
		if _, err := rand.Read(c.id[:]); err != nil {
			panic("xcodec: could not generate cache id: " + err.Error())
		}
	}
	return c
}

// NewMemoryCacheWithBytes mirrors the C++ constructor which takes a byte
// limit: limitBytes / SegmentLength segments, at least one when a
// non-zero limit smaller than a segment is requested.
func NewMemoryCacheWithBytes(id CacheID, limitBytes int64, opts ...MemoryCacheOption) *MemoryCache {
	limit := 0
	if limitBytes > 0 {
		limit = int(limitBytes / SegmentLength)
		if limit == 0 {
			limit = 1
		}
	}
	all := append([]MemoryCacheOption{WithCacheID(id), WithSegmentLimit(limit)}, opts...)
	return NewMemoryCache(all...)
}

func (c *MemoryCache) Lookup(hash uint64) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[hash]
	if !ok {
		return nil
	}
	if entry.element != nil {
		c.lru.MoveToFront(entry.element)
	}
	return entry.segment
}

func (c *MemoryCache) Has(hash uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[hash]
	return ok
}

func (c *MemoryCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *MemoryCache) Enter(hash uint64, segment []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enterLocked(hash, segment)
}

func (c *MemoryCache) Replace(hash uint64, segment []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[hash]; ok {
		entry.segment = cloneSegment(segment)
		if entry.element != nil {
			c.lru.MoveToFront(entry.element)
		}
		return
	}
	c.enterLocked(hash, segment)
}

func (c *MemoryCache) enterLocked(hash uint64, segment []byte) {
	if len(segment) != SegmentLength {
		panic("xcodec: cache segment must be exactly 2048 bytes")
	}
	if entry, ok := c.entries[hash]; ok {
		entry.segment = cloneSegment(segment)
		if entry.element != nil {
			c.lru.MoveToFront(entry.element)
		}
		return
	}
	if c.limit > 0 && len(c.entries) >= c.limit {
		c.evictOldestLocked()
	}
	entry := &memoryEntry{segment: cloneSegment(segment)}
	if c.limit > 0 {
		entry.element = c.lru.PushFront(hash)
	}
	c.entries[hash] = entry
}

func (c *MemoryCache) evictOldestLocked() {
	back := c.lru.Back()
	if back == nil {
		return
	}
	hash := back.Value.(uint64)
	c.lru.Remove(back)
	delete(c.entries, hash)
}

// Touch is a no-op for the memory cache beyond the LRU bump performed by
// Lookup, matching the C++ default.
func (c *MemoryCache) Touch(uint64, []byte) {}

func (c *MemoryCache) OutOfBand() bool { return false }

// Connect spins up (or reuses) a sibling memory cache of the same size
// for the given peer ID, exactly like the C++ XCodecMemoryCache.
func (c *MemoryCache) Connect(id CacheID) Cache {
	c.mu.Lock()
	defer c.mu.Unlock()
	if child, ok := c.children[id]; ok {
		return child
	}
	child := NewMemoryCache(WithCacheID(id), WithSegmentLimit(c.limit))
	c.children[id] = child
	return child
}

func (c *MemoryCache) ID() CacheID { return c.id }

// PairCache layers a primary cache over a secondary one, mirroring the
// C++ XCodecCachePair: entries are written to both levels, lookups check
// the primary first and promote secondary hits into the primary.
type PairCache struct {
	primary   Cache
	secondary Cache
}

// NewPairCache returns a cache pair.  Like the C++ version, the pair
// inherits the (most persistent) secondary's identity.
func NewPairCache(primary, secondary Cache) *PairCache {
	return &PairCache{primary: primary, secondary: secondary}
}

func (p *PairCache) Lookup(hash uint64) []byte {
	if seg := p.primary.Lookup(hash); seg != nil {
		// Let the secondary refresh its eviction state.
		p.secondary.Touch(hash, seg)
		return seg
	}
	seg := p.secondary.Lookup(hash)
	if seg != nil {
		p.primary.Enter(hash, seg)
	}
	return seg
}

func (p *PairCache) Enter(hash uint64, segment []byte) {
	p.primary.Enter(hash, segment)
	p.secondary.Enter(hash, segment)
}

func (p *PairCache) Replace(hash uint64, segment []byte) {
	p.primary.Replace(hash, segment)
	p.secondary.Replace(hash, segment)
}

func (p *PairCache) Touch(hash uint64, segment []byte) {
	p.primary.Touch(hash, segment)
	p.secondary.Touch(hash, segment)
}

func (p *PairCache) Has(hash uint64) bool {
	return p.primary.Has(hash) || p.secondary.Has(hash)
}

func (p *PairCache) Size() int { return p.primary.Size() + p.secondary.Size() }

func (p *PairCache) OutOfBand() bool { return p.primary.OutOfBand() }

func (p *PairCache) Connect(id CacheID) Cache {
	return NewPairCache(p.primary.Connect(id), p.secondary.Connect(id))
}

func (p *PairCache) ID() CacheID { return p.secondary.ID() }

// NullCache never stores anything; it mirrors the out-of-band null cache
// used by tack (-N), where the decoder is expected to share the
// encoder's pre-populated knowledge.
type NullCache struct {
	id CacheID
}

// NewNullCache returns an empty out-of-band cache.
func NewNullCache(id CacheID) *NullCache { return &NullCache{id: id} }

func (n *NullCache) Lookup(uint64) []byte     { return nil }
func (n *NullCache) Enter(uint64, []byte)     {}
func (n *NullCache) Replace(uint64, []byte)   {}
func (n *NullCache) Touch(uint64, []byte)     {}
func (n *NullCache) Has(uint64) bool          { return false }
func (n *NullCache) Size() int                { return 0 }
func (n *NullCache) OutOfBand() bool          { return true }
func (n *NullCache) Connect(id CacheID) Cache { return &NullCache{id: id} }
func (n *NullCache) ID() CacheID              { return n.id }

func cloneSegment(segment []byte) []byte {
	out := make([]byte, len(segment))
	copy(out, segment)
	return out
}
