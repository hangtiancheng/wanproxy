package xcodec

import (
	"container/list"
	"crypto/rand"
	"sync"
)

type CacheID [16]byte

type Cache interface {
	Lookup(hash uint64) []byte

	Enter(hash uint64, segment []byte)

	Replace(hash uint64, segment []byte)

	Touch(hash uint64, segment []byte)

	Has(hash uint64) bool

	Size() int

	OutOfBand() bool

	Connect(id CacheID) Cache

	ID() CacheID
}

type memoryEntry struct {
	segment []byte
	element *list.Element
}

const DefaultMemoryCacheBytes = 64 << 20

type memoryKey struct {
	id   CacheID
	hash uint64
}

type memoryStore struct {
	mu      sync.Mutex
	limit   int
	entries map[memoryKey]*memoryEntry
	counts  map[CacheID]int
	lru     *list.List
}

type MemoryCache struct {
	id    CacheID
	store *memoryStore
}

type MemoryCacheOption func(*MemoryCache)

func WithSegmentLimit(n int) MemoryCacheOption {
	return func(c *MemoryCache) {
		if n > 0 {
			c.store.limit = n
		}
	}
}

func WithCacheID(id CacheID) MemoryCacheOption {
	return func(c *MemoryCache) { c.id = id }
}

func NewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	c := &MemoryCache{
		store: &memoryStore{
			limit:   DefaultMemoryCacheBytes / SegmentLength,
			entries: make(map[memoryKey]*memoryEntry),
			counts:  make(map[CacheID]int),
			lru:     list.New(),
		},
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

func NewMemoryCacheWithBytes(id CacheID, limitBytes int64, opts ...MemoryCacheOption) *MemoryCache {
	limit := 0
	if limitBytes > 0 {
		limit = int(min(limitBytes/SegmentLength, int64(int(^uint(0)>>1))))
		if limit == 0 {
			limit = 1
		}
	}
	all := append([]MemoryCacheOption{WithCacheID(id), WithSegmentLimit(limit)}, opts...)
	return NewMemoryCache(all...)
}

func (c *MemoryCache) Lookup(hash uint64) []byte {
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[memoryKey{c.id, hash}]
	if !ok {
		return nil
	}
	s.lru.MoveToFront(entry.element)
	return entry.segment
}

func (c *MemoryCache) Has(hash uint64) bool {
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[memoryKey{c.id, hash}]
	return ok
}

func (c *MemoryCache) Size() int {
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[c.id]
}

func (c *MemoryCache) Enter(hash uint64, segment []byte) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.enterLocked(hash, segment)
}

func (c *MemoryCache) Replace(hash uint64, segment []byte) {
	c.Enter(hash, segment)
}

func (c *MemoryCache) enterLocked(hash uint64, segment []byte) {
	if len(segment) != SegmentLength {
		panic("xcodec: cache segment must be exactly 2048 bytes")
	}
	s := c.store
	key := memoryKey{c.id, hash}
	if entry, ok := s.entries[key]; ok {
		entry.segment = cloneSegment(segment)
		s.lru.MoveToFront(entry.element)
		return
	}
	if len(s.entries) >= s.limit {
		s.evictOldestLocked()
	}
	entry := &memoryEntry{segment: cloneSegment(segment)}
	entry.element = s.lru.PushFront(key)
	s.entries[key] = entry
	s.counts[c.id]++
}

func (s *memoryStore) evictOldestLocked() {
	back := s.lru.Back()
	if back == nil {
		return
	}
	key := back.Value.(memoryKey)
	s.lru.Remove(back)
	delete(s.entries, key)
	s.counts[key.id]--
	if s.counts[key.id] == 0 {
		delete(s.counts, key.id)
	}
}

func (c *MemoryCache) Touch(uint64, []byte) {}

func (c *MemoryCache) OutOfBand() bool { return false }

func (c *MemoryCache) Connect(id CacheID) Cache {
	// Namespace handles carry no retained peer registry. Every namespace
	// shares the same LRU and budget, including the local encoder cache.
	return &MemoryCache{id: id, store: c.store}
}

func (c *MemoryCache) ID() CacheID { return c.id }

type PairCache struct {
	primary   Cache
	secondary Cache
}

func NewPairCache(primary, secondary Cache) *PairCache {
	return &PairCache{primary: primary, secondary: secondary}
}

func (p *PairCache) Lookup(hash uint64) []byte {
	if seg := p.primary.Lookup(hash); seg != nil {
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

type NullCache struct {
	id CacheID
}

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
