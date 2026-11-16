package xcodec

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestXCDFSConcurrentOpenPublishesInitializedDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.cache")
	const count = 16
	disks := make(chan *Disk, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			disk, err := OpenDisk(path, testVolumeSize(2), nil)
			if err != nil {
				t.Error(err)
				return
			}
			if disk.Local() == nil {
				t.Error("published an uninitialized disk")
			}
			disks <- disk
		})
	}
	wg.Wait()
	close(disks)
	var first *Disk
	for disk := range disks {
		if first == nil {
			first = disk
		}
		if disk != first {
			t.Error("concurrent opens did not share the disk")
		}
		if err := disk.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestXCDFSRejectsResizeWithoutTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.cache")
	contents := bytes.Repeat([]byte{0x42}, int(testVolumeSize(2)))
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDisk(path, testVolumeSize(3), nil); err == nil {
		t.Fatal("accepted volume resize")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatal("rejected open changed the volume")
	}
}

func TestXCDFSHardLinksShareWriter(t *testing.T) {
	dir := t.TempDir()
	path, alias := filepath.Join(dir, "volume"), filepath.Join(dir, "alias")
	disk, err := OpenDisk(path, testVolumeSize(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := os.Link(path, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	other, err := OpenDisk(alias, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other != disk {
		t.Fatal("hard link created a competing volume writer")
	}
	if _, err := OpenDisk(alias, testVolumeSize(3), nil); err == nil {
		t.Fatal("resized open volume through alias")
	}
}

func TestXCDFSOverflowPeersShareBoundedFallback(t *testing.T) {
	disk, err := OpenDisk(filepath.Join(t.TempDir(), "volume"), testVolumeSize(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	disk.fallback = NewMemoryCache(WithSegmentLimit(2))
	// Simulate an exhausted registry without expensive unrelated disk I/O.
	for id := uint16(0); id < xcdfsXUIDCount; id++ {
		disk.xuidCaches[id] = disk.Local()
	}
	seg := testSegment(1)
	for i := range 10 {
		peer := disk.Local().Connect(CacheID{byte(i + 1)})
		peer.Enter(HashSegment(seg), seg)
	}
	if len(disk.fallback.store.entries) != 2 {
		t.Fatal("registry exhaustion bypassed fallback budget")
	}
}

func TestXCDFSCorruptRegistryReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.cache")
	contents := make([]byte, testVolumeSize(2))
	contents[16] = 1
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDisk(path, 0, nil); err == nil {
		t.Fatal("accepted missing local identity")
	}
}

func TestXCDFSFailedWriteDoesNotGrowIndex(t *testing.T) {
	disk, err := OpenDisk(filepath.Join(t.TempDir(), "failed.cache"), testVolumeSize(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	local := disk.Local()
	disk.file.Close()
	before := len(disk.indexBlock)
	for range xcdfsEntriesPerIndexBlock * 2 {
		if err := disk.enter(local, 1, testSegment(1)); err == nil {
			t.Fatal("write unexpectedly succeeded")
		}
	}
	if len(disk.indexBlock) != before || disk.indexBlockNext != 0 {
		t.Fatal("failed writes changed the index")
	}
}

func TestXCDFSVolumeLockAcrossProcesses(t *testing.T) {
	if path := os.Getenv("WANPROXY_CACHE_LOCK_TEST"); path != "" {
		disk, err := OpenDisk(path, 0, nil)
		if err == nil {
			disk.Close()
			t.Fatal("opened volume already locked by another process")
		}
		if !strings.Contains(err.Error(), "lock volume") {
			t.Fatalf("unexpected open failure: %v", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "locked.cache")
	disk, err := OpenDisk(path, testVolumeSize(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestXCDFSVolumeLockAcrossProcesses$")
	child.Env = append(os.Environ(), "WANPROXY_CACHE_LOCK_TEST="+path)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDisk(path, 0, nil)
	if err != nil {
		t.Fatalf("lock not released by Close: %v", err)
	}
	reopened.Close()
}
