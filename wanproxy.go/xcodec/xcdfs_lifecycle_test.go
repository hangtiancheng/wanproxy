package xcodec

import (
	"bytes"
	"os"
	"path/filepath"
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
