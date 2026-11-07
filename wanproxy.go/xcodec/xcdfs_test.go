package xcodec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func testVolumeSize(indexBlocks int) uint64 {
	blocks := uint64(xcdfsRegistryBlocks) + uint64(indexBlocks)*(1+xcdfsEntriesPerIndexBlock)
	return blocks * xcdfsBlockSize
}

func testSegment(b byte) []byte {
	return bytes.Repeat([]byte{b}, SegmentLength)
}

func TestXCDFSBasicOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.xcache")
	disk, err := OpenDisk(path, testVolumeSize(4), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer disk.Close()

	local := disk.Local()
	if local == nil {
		t.Fatalf("no local cache")
	}
	if local.ID() == (CacheID{}) {
		t.Fatalf("local cache has no UUID")
	}
	if local.OutOfBand() {
		t.Fatalf("disk cache must be in-band")
	}

	seg := testSegment(0x11)
	hash := HashSegment(seg)
	if local.Has(hash) || local.Lookup(hash) != nil {
		t.Fatalf("empty cache returned a segment")
	}

	local.Enter(hash, seg)
	if !local.Has(hash) {
		t.Fatalf("enter did not register the hash")
	}
	if got := local.Lookup(hash); !bytes.Equal(got, seg) {
		t.Fatalf("lookup mismatch")
	}
	if local.Size() != 1 {
		t.Fatalf("size = %d, want 1", local.Size())
	}

	seg2 := testSegment(0x22)
	local.Replace(hash, seg2)
	if got := local.Lookup(hash); got != nil {
		t.Fatalf("lookup of name-reused block should fail verification")
	}

	local.Enter(hash, seg)
	offset, ok := local.hashCode[hash]
	if !ok {
		t.Fatalf("hash not tracked")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xff}, 64), int64(offset*xcdfsBlockSize)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := local.Lookup(hash); got != nil {
		t.Fatalf("lookup of corrupted block should fail, got %d bytes", len(got))
	}
	if local.Has(hash) {
		t.Fatalf("corrupted entry should have been removed")
	}
}

func TestXCDFSReopenReloadsEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.xcache")

	disk, err := OpenDisk(path, testVolumeSize(4), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	local := disk.Local()
	segs := make([][]byte, xcdfsEntriesPerIndexBlock+5)
	hashes := make([]uint64, len(segs))
	for i := range segs {
		segs[i] = make([]byte, SegmentLength)
		for j := range segs[i] {
			segs[i][j] = byte(i*7 + j)
		}
		hashes[i] = HashSegment(segs[i])
		local.Enter(hashes[i], segs[i])
	}
	if err := disk.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := disk.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	disk2, err := OpenDisk(path, 0, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer disk2.Close()
	local2 := disk2.Local()
	if local2.Size() != xcdfsEntriesPerIndexBlock {
		t.Fatalf("reloaded size = %d, want %d", local2.Size(), xcdfsEntriesPerIndexBlock)
	}
	for i := 0; i < xcdfsEntriesPerIndexBlock; i++ {
		if got := local2.Lookup(hashes[i]); !bytes.Equal(got, segs[i]) {
			t.Fatalf("reloaded segment %d mismatch", i)
		}
	}
	for i := xcdfsEntriesPerIndexBlock; i < len(segs); i++ {
		if local2.Has(hashes[i]) {
			t.Fatalf("segment %d from the unwritten in-flight index block should be absent", i)
		}
	}
}

func TestXCDFSFIFOWraparound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo.xcache")
	const indexBlocks = 2
	disk, err := OpenDisk(path, testVolumeSize(indexBlocks), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer disk.Close()
	local := disk.Local()

	capacity := indexBlocks * xcdfsEntriesPerIndexBlock
	for i := 0; i < capacity*2; i++ {
		seg := make([]byte, SegmentLength)
		seg[0] = byte(i)
		seg[SegmentLength-1] = byte(i >> 8)
		local.Enter(HashSegment(seg), seg)
	}

	if local.Size() > capacity {
		t.Fatalf("size %d exceeds capacity %d after wraparound", local.Size(), capacity)
	}
	newest := make([]byte, SegmentLength)
	newest[0] = byte(capacity*2 - 1)
	newest[SegmentLength-1] = byte((capacity*2 - 1) >> 8)
	if got := local.Lookup(HashSegment(newest)); !bytes.Equal(got, newest) {
		t.Fatalf("newest segment lost after wraparound")
	}
	oldest := make([]byte, SegmentLength)
	if local.Has(HashSegment(oldest)) {
		t.Fatalf("oldest segment survived wraparound")
	}
}

func TestXCDFSConnectIsolatesXUIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xuid.xcache")
	disk, err := OpenDisk(path, testVolumeSize(4), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer disk.Close()

	var peerA, peerB CacheID
	peerA[0] = 0xaa
	peerB[0] = 0xbb

	cacheA, err := disk.Connect(peerA)
	if err != nil {
		t.Fatalf("connect A: %v", err)
	}
	cacheB, err := disk.Connect(peerB)
	if err != nil {
		t.Fatalf("connect B: %v", err)
	}
	if cacheA == cacheB {
		t.Fatalf("distinct peers must get distinct front-ends")
	}
	again, err := disk.Connect(peerA)
	if err != nil || again != cacheA {
		t.Fatalf("reconnect must reuse the front-end")
	}
	if cacheA.xuid == cacheB.xuid || cacheA.xuid == xcdfsXUIDLocal {
		t.Fatalf("bad xuid assignment: A=%d B=%d", cacheA.xuid, cacheB.xuid)
	}

	seg := testSegment(0x33)
	hash := HashSegment(seg)
	cacheA.Enter(hash, seg)
	filler := make([][]byte, xcdfsEntriesPerIndexBlock)
	for i := range filler {
		filler[i] = make([]byte, SegmentLength)
		filler[i][0] = byte(i)
		filler[i][1] = 0xf0
		cacheA.Enter(HashSegment(filler[i]), filler[i])
	}
	if cacheB.Has(hash) {
		t.Fatalf("peer B must not see peer A's segments")
	}
	if !cacheA.Has(hash) {
		t.Fatalf("peer A lost its segment")
	}

	disk.Sync()
	disk.Close()

	disk2, err := OpenDisk(path, 0, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer disk2.Close()
	cacheA2, err := disk2.Connect(peerA)
	if err != nil {
		t.Fatalf("reconnect A: %v", err)
	}
	cacheB2, err := disk2.Connect(peerB)
	if err != nil {
		t.Fatalf("reconnect B: %v", err)
	}
	if !cacheA2.Has(hash) {
		t.Fatalf("peer A entry did not survive reopen")
	}
	if cacheB2.Has(hash) {
		t.Fatalf("peer B must not see peer A's entries after reopen")
	}
}

func TestXCDFSCorruptBoundaryBlockDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.xcache")
	disk, err := OpenDisk(path, testVolumeSize(4), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	local := disk.Local()

	seg := testSegment(0x44)
	hash := HashSegment(seg)
	local.Enter(hash, seg)
	offset := local.hashCode[hash]
	disk.Sync()
	disk.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0x00}, SegmentLength), int64(offset*xcdfsBlockSize)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	disk2, err := OpenDisk(path, 0, nil)
	if err != nil {
		t.Fatalf("reopen after corruption: %v", err)
	}
	defer disk2.Close()
	if disk2.Local().Has(hash) {
		t.Fatalf("corrupt entry should have been dropped during checked load")
	}
}

func TestXCDFSSharedOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.xcache")
	d1, err := OpenDisk(path, testVolumeSize(2), nil)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	d2, err := OpenDisk(path, testVolumeSize(2), nil)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("same path must share one Disk")
	}
	d1.Close()
	seg := testSegment(0x31)
	d2.Local().Enter(HashSegment(seg), seg)
	if got := d2.Local().Lookup(HashSegment(seg)); !bytes.Equal(got, seg) {
		t.Fatal("closing one owner broke the other shared handle")
	}
	d2.Close()
}

func TestXCDFSTooSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.xcache")
	if _, err := OpenDisk(path, 4096, nil); err == nil {
		t.Fatalf("expected too-small error")
	}
}

func TestXCDFSPairWithMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pair.xcache")
	disk, err := OpenDisk(path, testVolumeSize(2), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer disk.Close()

	mem := NewMemoryCache(WithSegmentLimit(1))
	pair := NewPairCache(mem, disk.Local())

	s1 := testSegment(0x51)
	s2 := testSegment(0x52)
	h1, h2 := HashSegment(s1), HashSegment(s2)

	pair.Enter(h1, s1)
	pair.Enter(h2, s2)

	if got := pair.Lookup(h1); !bytes.Equal(got, s1) {
		t.Fatalf("pair lookup should fall back to disk for h1")
	}
	if !mem.Has(h1) {
		t.Fatalf("disk hit should be promoted into the primary")
	}
}
