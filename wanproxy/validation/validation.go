package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"

	"github.com/hangtiancheng/wanproxy/wanproxy/xcodec"
)

const (
	GiB = 1024 * 1024 * 1024
	MiB = 1024 * 1024
)

type Result struct {
	File           string
	Bytes          int64
	OriginalSHA256 string
	DecodedSHA256  string
	Verified       bool
}

func CreateRandomFile(path string, size int64, seed int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, MiB)
	var written int64
	for written < size {
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		for i := int64(0); i < n; i++ {
			buf[i] = byte(rng.Intn(256))
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		written += n
	}
	return f.Sync()
}

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func VerifyXCodecFile(path string, cacheSegments int) (*Result, error) {
	original, err := HashFile(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	encCache := xcodec.NewMemoryCache(xcodec.WithSegmentLimit(cacheSegments))
	decCache := xcodec.NewMemoryCache(xcodec.WithSegmentLimit(cacheSegments))

	encoded := xcodec.NewEncodeReader(f, encCache)
	decoded := xcodec.NewDecodeReader(encoded, decCache)

	h := sha256.New()
	n, err := io.Copy(h, decoded)
	if err != nil {
		return nil, fmt.Errorf("xcodec roundtrip failed: %w", err)
	}

	decodedHash := hex.EncodeToString(h.Sum(nil))
	return &Result{
		File:           path,
		Bytes:          n,
		OriginalSHA256: original,
		DecodedSHA256:  decodedHash,
		Verified:       original == decodedHash,
	}, nil
}
