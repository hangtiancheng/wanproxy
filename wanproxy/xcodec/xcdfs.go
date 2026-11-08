package xcodec

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	xcdfsBlockSize            = SegmentLength
	xcdfsXUIDLocal            = 0
	xcdfsXUIDCount            = 1024
	xcdfsRegistryBlocks       = (xcdfsXUIDCount * 16) / xcdfsBlockSize
	xcdfsRegistryBlockEntries = xcdfsBlockSize / 16
	xcdfsEntriesPerIndexBlock = (xcdfsBlockSize - 8) / (2 + 8)
	xcdfsCheckBoundary        = 80
)

type Disk struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	log      *slog.Logger
	refs     int
	writeErr error

	diskBlocks  uint64
	indexBlocks uint64

	xuidCaches map[uint16]*DiskCache
	uuidXUID   map[CacheID]uint16

	currentIndexBlock uint64
	indexBlock        []byte
	indexBlockNext    int
	indexBlockCounter uint64
}

type DiskCache struct {
	disk     *Disk
	uuid     CacheID
	xuid     uint16
	hashCode map[uint64]uint64
}

var (
	diskMapMu sync.Mutex
	diskMap   = make(map[string]*Disk)
)

func OpenDisk(path string, size uint64, log *slog.Logger) (*Disk, error) {
	if log == nil {
		log = slog.Default()
	}

	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(parent, filepath.Base(path))
	}
	if size > math.MaxInt64 || (size != 0 && size < (xcdfsRegistryBlocks+1+xcdfsEntriesPerIndexBlock)*xcdfsBlockSize) {
		return nil, errors.New("invalid XCDFS volume size")
	}
	diskMapMu.Lock()
	defer diskMapMu.Unlock()
	if d, ok := diskMap[path]; ok {
		if size != 0 && size/xcdfsBlockSize != d.diskBlocks {
			return nil, errors.New("cannot resize an open XCDFS volume")
		}
		d.refs++
		return d, nil
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open disk: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("could not stat disk: %w", err)
	}
	if size != 0 && st.Mode().IsRegular() {
		if st.Size() != 0 && uint64(st.Size()) != size {
			f.Close()
			return nil, errors.New("cannot resize an existing XCDFS volume")
		}
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
		refs:       1,
		log:        log.With("component", "/xcodec/disk"),
		diskBlocks: size / xcdfsBlockSize,
		xuidCaches: make(map[uint16]*DiskCache),
		uuidXUID:   make(map[CacheID]uint16),
	}
	if d.diskBlocks < xcdfsRegistryBlocks+1+xcdfsEntriesPerIndexBlock {
		f.Close()
		return nil, errors.New("disk too small for an XCDFS index and data group")
	}
	d.indexBlocks = (d.diskBlocks - xcdfsRegistryBlocks) / (1 + xcdfsEntriesPerIndexBlock)
	if err := d.load(); err != nil {
		f.Close()
		return nil, err
	}
	diskMap[path] = d
	return d, nil
}

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
		delete(counterIndex, counter)
	} else {
		d.log.Debug("this appears to be an empty disk; writing from start")
	}

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

	if d.xuidCaches[xcdfsXUIDLocal] == nil {
		if len(d.xuidCaches) != 0 {
			return errors.New("XCDFS registry has peer namespaces but no local identity")
		}
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

func (d *Disk) indexReadCounter(indexBlock uint64) (uint64, error) {
	block, err := d.blockRead(d.indexBlockAddress(indexBlock))
	if err != nil {
		return 0, fmt.Errorf("could not read index: %w", err)
	}
	return binary.LittleEndian.Uint64(block[:8]), nil
}

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

func (d *Disk) enter(cache *DiskCache, hash uint64, seg []byte) (err error) {
	if d.writeErr != nil {
		return d.writeErr
	}
	defer func() {
		if err != nil {
			d.writeErr = err
		}
	}()
	var entry [10]byte
	binary.LittleEndian.PutUint16(entry[:2], cache.xuid)
	binary.LittleEndian.PutUint64(entry[2:], hash)
	offset := d.dataBlockAddress(d.currentIndexBlock, d.indexBlockNext)
	if err := d.blockWrite(seg, offset); err != nil {
		return fmt.Errorf("could not write data segment: %w", err)
	}
	d.indexBlock = append(d.indexBlock, entry[:]...)
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
		if err := d.indexInvalidateEntries(d.currentIndexBlock); err != nil {
			return fmt.Errorf("could not invalidate new index block: %w", err)
		}

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
		return 0, errors.New("exhausted XUIDs for disk; cannot connect new UUID")
	}
	return xuid, nil
}

func (d *Disk) Local() *DiskCache {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.xuidCaches[xcdfsXUIDLocal]
}

func (d *Disk) Connect(uuid CacheID) (*DiskCache, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connect(uuid)
}

func (d *Disk) Path() string { return d.path }

func (d *Disk) Close() error {
	diskMapMu.Lock()
	defer diskMapMu.Unlock()
	if d.refs == 0 {
		return nil
	}
	d.refs--
	if d.refs > 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(diskMap, d.path)
	return d.file.Close()
}

func (d *Disk) Sync() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.Sync()
}

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

func (c *DiskCache) OutOfBand() bool { return false }

func (c *DiskCache) Connect(id CacheID) Cache {
	cache, err := c.disk.Connect(id)
	if err != nil {
		return NewMemoryCache(WithCacheID(id))
	}
	return cache
}

func (c *DiskCache) ID() CacheID { return c.uuid }

var _ Cache = (*DiskCache)(nil)
var _ io.Closer = (*Disk)(nil)
