package validation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyXCodecFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.bin")

	// Deterministic compressible-ish fixture.
	if err := CreateRandomFile(path, 4*MiB, 7); err != nil {
		t.Fatalf("create fixture: %v", err)
	}

	res, err := VerifyXCodecFile(path, 4096)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Verified {
		t.Fatalf("hash mismatch: original=%s decoded=%s", res.OriginalSHA256, res.DecodedSHA256)
	}
	if res.Bytes != 4*MiB {
		t.Fatalf("decoded %d bytes, want %d", res.Bytes, 4*MiB)
	}
}

func TestVerifyXCodecFileTinyCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.bin")
	if err := CreateRandomFile(path, 1*MiB, 11); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	// A cache far smaller than the stream: eviction must not break the
	// roundtrip because the encoder escapes what the decoder cannot know.
	res, err := VerifyXCodecFile(path, 2)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Verified {
		t.Fatalf("hash mismatch with tiny cache")
	}
}

func TestCreateRandomFileSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.bin")
	if err := CreateRandomFile(path, 1000, 3); err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 1000 {
		t.Fatalf("size = %d, want 1000", info.Size())
	}
}
