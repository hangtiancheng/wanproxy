package xcodec

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// XCDFS on-disk layout constants, mirroring xcodec_cache_disk.cc.
//
// The volume is a file (or device) of 2048-byte blocks:
//
//	[ registry: 8 blocks ][ index: N blocks ][ data: N*204 blocks ]
//
// The registry holds 1024 raw 16-byte UUID slots (128 per block) mapping
// XUIDs to peer cache namespaces.  Each index block holds a uint64
// monotonic counter followed by 204 (uint16 xuid, uint64 hash) entries
// describing the data blocks that follow it in FIFO order; a counter of
// zero marks a free index block.  Multi-byte fields are stored in
// little-endian order (the native order of every platform the C++
// implementation runs on).
const (
	xcdfsBlockSize            = SegmentLength // 2048
	xcdfsXUIDLocal            = 0
	xcdfsXUIDCount            = 1024
	xcdfsRegistryBlocks       = (xcdfsXUIDCount * 16) / xcdfsBlockSize // 8
	xcdfsRegistryBlockEntries = xcdfsBlockSize / 16                    // 128
	xcdfsEntriesPerIndexBlock = (xcdfsBlockSize - 8) / (2 + 8)         // 204
	xcdfsCheckBoundary        = 80
)

// Disk is the shared on-disk backend, mirroring the C++ XCodecDisk.  It
// is safe for concurrent use; all state and file access is serialized
// under one mutex.
type Disk struct {
	mu   sync.Mutex
	file *os.File
	path string
	log  *slog.Logger

	diskBlocks  uint64
	indexBlocks uint64

	xuidCaches map[uint16]*DiskCache
	uuidXUID   map[CacheID]uint16

	currentIndexBlock uint64
	indexBlock        []byte // in-flight index block (counter + entries)
	indexBlockNext    int
	indexBlockCounter uint64
}

// DiskCache is one XUID's front-end to a Disk, mirroring the C++
// XCodecDiskCache.  It implements the Cache interface.
type DiskCache struct {
	disk     *Disk
	uuid     CacheID
	xuid     uint16
	hashCode map[uint64]uint64 // hash -> data block number
}

var (
	diskMapMu sync.Mutex
	diskMap   = make(map[string]*Disk)
)

// OpenDisk opens (creating if necessary) an XCDFS volume at path.  When
// size is non-zero a regular file is truncated/extended to it; when size
// is zero the existing file size is used (as by tack -F).  Multiple
// opens of the same path share one Disk, like the C++ disk_map.
func OpenDisk(path string, size uint64, log *slog.Logger) (*Disk, error) {
	if log == nil {
		log = slog.Default()
	}

	diskMapMu.Lock()
	if d, ok := diskMap[path]; ok {
		diskMapMu.Unlock()
		log.Info("multiple distinct opens of disk; disk will be shared", "path", path)
		return d, nil
	}
	diskMapMu.Unlock()

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o700)
	if err != nil {
		return nil, fmt.Errorf("could not open disk: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("could not stat disk: %w", err)
	}
	if size != 0 && st.Mode().IsRegular() {
		if err := f.Truncate(int64(size)); err != nil {
			f.Close()
			return nil, fmt.Errorf("could not truncate/extend disk: %w", err)
		}
	}
	if size == 0 {
		size = uint64(st.Size())
		if size == 0 {
			f.Close()
			return nil, errors.New("could not determine disk size")
		}
	}

	d := &Disk{
		file:       f,
		path:       path,
		log:        log.With("component", "/xcodec/disk"),
		diskBlocks: size / xcdfsBlockSize,
		xuidCaches: make(map[uint16]*DiskCache),
		uuidXUID:   make(map[CacheID]uint16),
	}
	d.indexBlocks = (d.diskBlocks - xcdfsRegistryBlocks) / (1 + xcdfsEntriesPerIndexBlock)
	if d.indexBlocks == 0 {
		f.Close()
		return nil, errors.New("disk too small; reduce XCDFS_BLOCK_SIZE")
	}

	diskMapMu.Lock()
	if other, ok := diskMap[path]; ok {
		diskMapMu.Unlock()
		f.Close()
		return other, nil
	}
	diskMap[path] = d
	diskMapMu.Unlock()

	if err := d.load(); err != nil {
		diskMapMu.Lock()
		delete(diskMap, path)
		diskMapMu.Unlock()
		f.Close()
		return nil, err
	}
	return d, nil
}

// load performs the C++ XCodecDisk constructor sequence: load the
// registry, find the FIFO write head, load index entries in counter
// order (checking the boundary blocks), and collect unused registry
// entries.
func (d *Disk) load() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.log.Debug("opened disk",
		"indexBlocks", d.indexBlocks,
		"blockSize", xcdfsBlockSize,
		"dataBlocks", d.indexBlocks*xcdfsEntriesPerIndexBlock)

	if err := d.registryLoad(); err != nil {
		return fmt.Errorf("could not load registry and cannot recover: %w", err)
	}

	// Scan index block counters to order the FIFO and find the write
	// head: the first free block, or the lowest counter when full.
	counterIndex := make(map[uint64]uint64)
	for o := uint64(0); o < d.indexBlocks; o++ {
		counter, err := d.indexReadCounter(o)
		if err != nil {
			return fmt.Errorf("could not read counter for index block #%d: %w", o, err)
		}
		if counter == 0 {
			d.log.Debug("free index block found", "block", o)
			counterIndex[0] = o
			break
		}
		if counter >= d.indexBlockCounter {
			d.indexBlockCounter = counter + 1
		}
		counterIndex[counter] = o
	}

	if block, counter, ok := minCounterEntry(counterIndex); ok {
		if counter == 0 {
			d.log.Debug("write head is at first free index block", "block", block)
		} else {
			d.log.Debug("lowest counter found; this will be the write head", "block", block)
		}
		d.currentIndexBlock = block
		// Do not load entries from the index we are about to erase.
		delete(counterIndex, counter)
	} else {
		d.log.Debug("this appears to be an empty disk; writing from start")
	}

	// Load in counter order so the newest version of each hash wins,
	// checking the first and last XCDFS_CHECK_BOUNDARY blocks.
	leading := xcdfsCheckBoundary
	for len(counterIndex) > 0 {
		block, counter, _ := minCounterEntry(counterIndex)
		needCheck := false
		if leading != 0 {
			needCheck = true
			leading--
		} else if len(counterIndex) <= xcdfsCheckBoundary {
			needCheck = true
		}
		if err := d.indexLoadEntries(block, needCheck); err != nil {
			return fmt.Errorf("could not load index block #%d: %w", block, err)
		}
		delete(counterIndex, counter)
	}

	if err := d.registryCollect(); err != nil {
		return fmt.Errorf("could not collect unused entries from registry: %w", err)
	}

	if d.indexBlockCounter == 0 {
		d.indexBlockCounter = 1
	}
	d.indexBlock = nil
	d.appendIndexCounter()
	return nil
}

func minCounterEntry(m map[uint64]uint64) (block, counter uint64, ok bool) {
	first := true
	for c, b := range m {
		if first || c < counter {
			counter, block, ok, first = c, b, true, false
		}
	}
	return block, counter, ok
}

func (d *Disk) appendIndexCounter() {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], d.indexBlockCounter)
	d.indexBlock = append(d.indexBlock[:0], b[:]...)
	d.indexBlockNext = 0
}

// Block I/O.

func (d *Disk) blockRead(blockno uint64) ([]byte, error) {
	if blockno >= d.diskBlocks {
		return nil, fmt.Errorf("block %d out of range", blockno)
	}
	block := make([]byte, xcdfsBlockSize)
	if _, err := d.file.ReadAt(block, int64(blockno*xcdfsBlockSize)); err != nil {
		return nil, err
	}
	return block, nil
}

func (d *Disk) blockWrite(block []byte, blockno uint64) error {
	if blockno >= d.diskBlocks {
		return fmt.Errorf("block %d out of range", blockno)
	}
	if len(block) != xcdfsBlockSize {
		return fmt.Errorf("block write of %d bytes", len(block))
	}
	_, err := d.file.WriteAt(block, int64(blockno*xcdfsBlockSize))
	return err
}

func (d *Disk) indexBlockAddress(indexBlock uint64) uint64 {
	return xcdfsRegistryBlocks + indexBlock
}

func (d *Disk) dataBlockAddress(indexBlock uint64, entry int) uint64 {
	return xcdfsRegistryBlocks + d.indexBlocks +
		(indexBlock * xcdfsEntriesPerIndexBlock) + uint64(entry)
}

// Registry.

func (d *Disk) registryLoad() error {
	for r := 0; r < xcdfsRegistryBlocks; r++ {
		block, err := d.blockRead(uint64(r))
		if err != nil {
			return fmt.Errorf("could not read registry block for loading: %w", err)
		}
		for i := 0; i < xcdfsRegistryBlockEntries; i++ {
			xuid := uint16(r*xcdfsRegistryBlockEntries + i)
			var uuid CacheID
			copy(uuid[:], block[i*16:(i+1)*16])
			if uuid == (CacheID{}) {
				continue
			}
			if _, dup := d.uuidXUID[uuid]; dup {
				d.log.Error("duplicate XUID in registry", "xuid", xuid)
				continue
			}
			cache := &DiskCache{disk: d, uuid: uuid, xuid: xuid, hashCode: make(map[uint64]uint64)}
			d.xuidCaches[xuid] = cache
			d.uuidXUID[uuid] = xuid
		}
	}

	// Establish a local UUID if we do not have one.
	if len(d.xuidCaches) == 0 {
		var uuid CacheID
		if _, err := rand.Read(uuid[:]); err != nil {
			return fmt.Errorf("could not generate local UUID: %w", err)
		}
		if err := d.registryWrite(xcdfsXUIDLocal, uuid); err != nil {
			return fmt.Errorf("failed to write registry update for local UUID: %w", err)
		}
		cache := &DiskCache{disk: d, uuid: uuid, xuid: xcdfsXUIDLocal, hashCode: make(map[uint64]uint64)}
		d.xuidCaches[xcdfsXUIDLocal] = cache
		d.uuidXUID[uuid] = xcdfsXUIDLocal
	}
	d.log.Info("disk local UUID", "uuid", fmt.Sprintf("%x", d.xuidCaches[xcdfsXUIDLocal].uuid))
	return nil
}

func (d *Disk) registryWrite(xuid uint16, uuid CacheID) error {
	if xuid >= xcdfsXUIDCount {
		return fmt.Errorf("xuid %d out of range", xuid)
	}
	blockno := uint64(xuid / xcdfsRegistryBlockEntries)
	block, err := d.blockRead(blockno)
	if err != nil {
		return fmt.Errorf("could not read registry block for update: %w", err)
	}
	off := int(xuid%xcdfsRegistryBlockEntries) * 16
	copy(block[off:off+16], uuid[:])
	// NB: the C++ implementation writes the updated block back to
	// block 0 regardless of the XUID, corrupting the registry for
	// XUIDs beyond the first block; write to the correct block.
	if err := d.blockWrite(block, blockno); err != nil {
		return fmt.Errorf("failed to write registry update: %w", err)
	}
	return nil
}

func (d *Disk) registryCollect() error {
	for xuid := uint16(0); xuid < xcdfsXUIDCount; xuid++ {
		cache, ok := d.xuidCaches[xuid]
		if !ok {
			continue
		}
		if len(cache.hashCode) > 0 {
			d.log.Debug("xuid has entries", "xuid", xuid, "entries", len(cache.hashCode))
			continue
		}
		// Do not garbage collect the local XUID.
		if xuid == xcdfsXUIDLocal {
			continue
		}
		d.log.Info("xuid has no associated entries; removing entry", "xuid", xuid)
		if err := d.registryWrite(xuid, CacheID{}); err != nil {
			return fmt.Errorf("could not clear unused XUID #%d: %w", xuid, err)
		}
		delete(d.uuidXUID, cache.uuid)
		delete(d.xuidCaches, xuid)
	}
	return nil
}

// Index.

func (d *Disk) indexReadCounter(indexBlock uint64) (uint64, error) {
	block, err := d.blockRead(d.indexBlockAddress(indexBlock))
	if err != nil {
		return 0, fmt.Errorf("could not read index: %w", err)
	}
	return binary.LittleEndian.Uint64(block[:8]), nil
}

// indexLoadEntries loads one index block's entries into the per-XUID
// in-memory maps.  When check is set, each entry's data block is read
// and hash-verified; a mismatch inside the write-head window resets the
// write head onto the corrupt block, exactly as the C++ loader does.
func (d *Disk) indexLoadEntries(indexBlock uint64, check bool) error {
	block, err := d.blockRead(d.indexBlockAddress(indexBlock))
	if err != nil {
		return fmt.Errorf("could not read index to be loaded: %w", err)
	}
	counter := binary.LittleEndian.Uint64(block[:8])
	if counter == 0 {
		return errors.New("block became free during index load")
	}

	for i := 0; i < xcdfsEntriesPerIndexBlock; i++ {
		off := 8 + i*10
		xuid := binary.LittleEndian.Uint16(block[off : off+2])
		hash := binary.LittleEndian.Uint64(block[off+2 : off+10])
		if hash == 0 {
			continue
		}
		cache, ok := d.xuidCaches[xuid]
		if !ok {
			// A 0 XUID is valid, but also could be unused space.
			if xuid != 0 {
				d.log.Info("bogus XUID found in index block", "xuid", xuid)
			}
			continue
		}
		if _, exists := cache.hashCode[hash]; exists {
			d.log.Info("replacing previous cache entry")
			delete(cache.hashCode, hash)
		}

		offset := d.dataBlockAddress(indexBlock, i)
		if check {
			seg, err := d.blockRead(offset)
			if err != nil {
				return fmt.Errorf("could not read data entry for check: %w", err)
			}
			if HashSegment(seg) != hash {
				d.log.Info("removing invalid cache entry during check")
				// Reset the write head so the corrupt blocks are
				// rewritten, and stop loading this block.
				if d.currentIndexBlock > indexBlock {
					if err := d.indexInvalidateEntries(indexBlock); err != nil {
						return fmt.Errorf("could not remove invalid index entries: %w", err)
					}
					d.log.Debug("resetting current index to rewrite index block with errors")
					d.currentIndexBlock = indexBlock
					return nil
				}
				continue
			}
		}
		cache.hashCode[hash] = offset
	}
	return nil
}

// indexInvalidateEntries drops in-memory entries that point into the
// given index block's data blocks, mirroring the C++ invalidation.
func (d *Disk) indexInvalidateEntries(indexBlock uint64) error {
	block, err := d.blockRead(d.indexBlockAddress(indexBlock))
	if err != nil {
		return fmt.Errorf("could not read index to be invalidated: %w", err)
	}
	if binary.LittleEndian.Uint64(block[:8]) == 0 {
		d.log.Debug("skipping invalidate for free index")
		return nil
	}
	for i := 0; i < xcdfsEntriesPerIndexBlock; i++ {
		off := 8 + i*10
		xuid := binary.LittleEndian.Uint16(block[off : off+2])
		hash := binary.LittleEndian.Uint64(block[off+2 : off+10])
		if hash == 0 {
			continue
		}
		cache, ok := d.xuidCaches[xuid]
		if !ok {
			continue
		}
		offset, ok := cache.hashCode[hash]
		if !ok {
			d.log.Debug("skipping invalidate for absent hash")
			continue
		}
		if offset != d.dataBlockAddress(indexBlock, i) {
			d.log.Debug("skipping invalidate for old, inactive hash")
			continue
		}
		delete(cache.hashCode, hash)
	}
	return nil
}

// Cache front-end operations, called with d.mu held by the DiskCache
// methods below.

func (d *Disk) enter(cache *DiskCache, hash uint64, seg []byte) error {
	var entry [10]byte
	binary.LittleEndian.PutUint16(entry[:2], cache.xuid)
	binary.LittleEndian.PutUint64(entry[2:], hash)
	d.indexBlock = append(d.indexBlock, entry[:]...)

	offset := d.dataBlockAddress(d.currentIndexBlock, d.indexBlockNext)
	if err := d.blockWrite(seg, offset); err != nil {
		return fmt.Errorf("could not write data segment: %w", err)
	}
	cache.hashCode[hash] = offset

	d.indexBlockNext++
	if d.indexBlockNext == xcdfsEntriesPerIndexBlock {
		d.log.Debug("filled index block; writing to disk")
		if len(d.indexBlock) != xcdfsBlockSize {
			d.indexBlock = d.indexBlock[:xcdfsBlockSize]
		}
		if err := d.blockWrite(d.indexBlock, d.indexBlockAddress(d.currentIndexBlock)); err != nil {
			return fmt.Errorf("failed to write index block update; expect inconsistency: %w", err)
		}

		d.currentIndexBlock++
		if d.currentIndexBlock == d.indexBlocks {
			d.currentIndexBlock = 0
		}
		// We are going to be rewriting the entries associated with
		// the new index block; purge them from memory.
		if err := d.indexInvalidateEntries(d.currentIndexBlock); err != nil {
			d.log.Error("could not invalidate new index block; expect inconsistency", "error", err)
		}

		// A counter of 0 always indicates unused.
		d.indexBlockCounter++
		if d.indexBlockCounter == 0 {
			d.indexBlockCounter = 1
		}
		d.appendIndexCounter()
	}
	return nil
}

func (d *Disk) lookup(cache *DiskCache, hash uint64) []byte {
	offset, ok := cache.hashCode[hash]
	if !ok {
		return nil
	}
	seg, err := d.blockRead(offset)
	if err != nil {
		d.log.Error("could not read segment from disk; removing index entry", "error", err)
		delete(cache.hashCode, hash)
		return nil
	}
	if HashSegment(seg) != hash {
		d.log.Error("hash mismatch on disk; removing index entry")
		delete(cache.hashCode, hash)
		return nil
	}
	return seg
}

func (d *Disk) connect(uuid CacheID) (*DiskCache, error) {
	if xuid, ok := d.uuidXUID[uuid]; ok {
		return d.xuidCaches[xuid], nil
	}

	xuid, err := d.allocXUID()
	if err != nil {
		return nil, err
	}
	if err := d.registryWrite(xuid, uuid); err != nil {
		return nil, fmt.Errorf("failed to write registry update for new UUID: %w", err)
	}
	cache := &DiskCache{disk: d, uuid: uuid, xuid: xuid, hashCode: make(map[uint64]uint64)}
	d.xuidCaches[xuid] = cache
	d.uuidXUID[uuid] = xuid
	return cache, nil
}

func (d *Disk) allocXUID() (uint16, error) {
	find := func() uint16 {
		for xuid := uint16(0); xuid < xcdfsXUIDCount; xuid++ {
			if _, ok := d.xuidCaches[xuid]; !ok {
				return xuid
			}
		}
		return xcdfsXUIDCount
	}
	xuid := find()
	if xuid == xcdfsXUIDCount {
		if err := d.registryCollect(); err != nil {
			return 0, fmt.Errorf("failed to collect registry under XUID exhaustion: %w", err)
		}
		xuid = find()
		if xuid == xcdfsXUIDCount {
			return 0, errors.New("exhausted XUIDs for disk; cannot connect new UUID")
		}
	}
	return xuid, nil
}

// Disk front-end.

// Local returns the volume's local (XUID 0) cache front-end, like the
// C++ XCodecDisk::local().
func (d *Disk) Local() *DiskCache {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.xuidCaches[xcdfsXUIDLocal]
}

// Connect returns the cache front-end for a peer UUID, allocating a new
// XUID when necessary, like the C++ XCodecDisk::connect().
func (d *Disk) Connect(uuid CacheID) (*DiskCache, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connect(uuid)
}

// Path returns the volume path.
func (d *Disk) Path() string { return d.path }

// Close flushes and closes the volume, removing it from the shared map.
func (d *Disk) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	diskMapMu.Lock()
	if cur, ok := diskMap[d.path]; ok && cur == d {
		delete(diskMap, d.path)
	}
	diskMapMu.Unlock()
	return d.file.Close()
}

// Sync flushes the volume to stable storage.
func (d *Disk) Sync() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.Sync()
}

// Cache interface implementation for DiskCache.

func (c *DiskCache) Lookup(hash uint64) []byte {
	d := c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lookup(c, hash)
}

func (c *DiskCache) Enter(hash uint64, segment []byte) {
	if len(segment) != SegmentLength {
		panic("xcodec: cache segment must be exactly 2048 bytes")
	}
	d := c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.enter(c, hash, segment); err != nil {
		d.log.Error("enter failed", "error", err)
	}
}

func (c *DiskCache) Replace(hash uint64, segment []byte) {
	if len(segment) != SegmentLength {
		panic("xcodec: cache segment must be exactly 2048 bytes")
	}
	d := c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(c.hashCode, hash)
	if err := d.enter(c, hash, segment); err != nil {
		d.log.Error("replace failed", "error", err)
	}
}

func (c *DiskCache) Touch(hash uint64, segment []byte) {
	d := c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	// Do nothing if this is already in the cache; otherwise we have
	// lost track of the entry and must reenter it.
	if _, ok := c.hashCode[hash]; ok {
		return
	}
	if len(segment) != SegmentLength {
		return
	}
	if err := d.enter(c, hash, segment); err != nil {
		d.log.Error("touch failed", "error", err)
	}
}

func (c *DiskCache) Has(hash uint64) bool {
	d := c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := c.hashCode[hash]
	return ok
}

func (c *DiskCache) Size() int {
	d := c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(c.hashCode)
}

// OutOfBand is false: like the C++ disk cache, references must be
// extracted in-stream.
func (c *DiskCache) OutOfBand() bool { return false }

func (c *DiskCache) Connect(id CacheID) Cache {
	cache, err := c.disk.Connect(id)
	if err != nil {
		// Fall back to an isolated memory cache rather than failing
		// the stream; the session will ASK/LEARN as needed.
		return NewMemoryCache(WithCacheID(id))
	}
	return cache
}

func (c *DiskCache) ID() CacheID { return c.uuid }

var _ Cache = (*DiskCache)(nil)
var _ io.Closer = (*Disk)(nil)
