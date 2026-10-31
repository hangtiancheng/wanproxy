// Command diskdup is the Go port of the C++ diskdup program: a block
// storage demonstration program that makes two block devices or files
// have identical contents with a minimal number of writes.
//
// Usage: diskdup [-s block_size] source_volume target_volume
//
// The volumes are processed block by block: each block is read from
// both volumes and compared, and only non-identical blocks are written
// to the target.  Processing stops at the end of the shorter volume; a
// volume ending in the middle of a block is reported as a non-empty
// end-of-stream, like the C++ implementation.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
)

const defaultBlockSize = 65536

// errPartialBlock mirrors the C++ non-empty end-of-stream condition:
// one of the volumes ended in the middle of a block, which usually
// means the block size does not match the volumes.
var errPartialBlock = errors.New("received non-empty end-of-stream")

var errUsage = errors.New("usage error")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if err == errUsage {
			usage()
		}
		fmt.Fprintf(os.Stderr, "diskdup: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: diskdup [-s block_size] source_volume target_volume")
	os.Exit(1)
}

func run(args []string) error {
	blockSize := int64(defaultBlockSize)
	var files []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			files = append(files, args[i+1:]...)
			break
		}
		if len(arg) >= 2 && arg[0] == '-' && arg != "-" {
			for j := 1; j < len(arg); j++ {
				switch arg[j] {
				case 's':
					var value string
					if j+1 < len(arg) {
						value = arg[j+1:]
						j = len(arg)
					} else if i+1 < len(args) {
						i++
						value = args[i]
					} else {
						return errUsage
					}
					size, err := strconv.ParseInt(value, 10, 64)
					if err != nil || size <= 0 {
						return errUsage
					}
					blockSize = size
				default:
					return errUsage
				}
			}
			continue
		}
		files = append(files, arg)
	}

	if len(files) != 2 {
		return errUsage
	}

	// The C++ version opens the source read-only and the target
	// read-write; the target must already exist.
	source, err := os.Open(files[0])
	if err != nil {
		return fmt.Errorf("could not open source volume: %w", err)
	}
	defer source.Close()

	target, err := os.OpenFile(files[1], os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("could not open target volume: %w", err)
	}
	defer target.Close()

	// Like the C++ program, diskdup installs no log masks, so every
	// message, including the per-block debug lines, is logged.
	common.ResetMasks()
	log := common.Component("/diskdup")

	_, err = dupBlocks(log, source, target, blockSize)
	return err
}

// Stats describes one diskdup run.
type Stats struct {
	// Compared is the number of blocks read from both volumes.
	Compared int64

	// Written is the number of blocks written to the target, i.e. the
	// blocks that were not already identical.
	Written int64
}

// blockVolume is the block device interface diskdup needs: block-sized
// reads and writes at arbitrary offsets, like the C++ BlockHandle.
type blockVolume interface {
	io.ReaderAt
	io.WriterAt
}

// dupBlocks mirrors the C++ DiskDup loop: read block N from both
// volumes, write the source block to the target only when it differs,
// and stop at the end of the shorter volume.
func dupBlocks(log *slog.Logger, source, target blockVolume, blockSize int64) (Stats, error) {
	var stats Stats
	sourceBlock := make([]byte, blockSize)
	targetBlock := make([]byte, blockSize)

	for block := int64(0); ; block++ {
		offset := block * blockSize

		n, err := readBlock(source, sourceBlock, offset)
		if err != nil {
			reportReadError(log, err, "source")
			return stats, err
		}
		if n == 0 {
			break
		}

		n, err = readBlock(target, targetBlock, offset)
		if err != nil {
			reportReadError(log, err, "target")
			return stats, err
		}
		if n == 0 {
			break
		}

		stats.Compared++
		if bytes.Equal(sourceBlock[:n], targetBlock[:n]) {
			log.Debug("identical block, incrementing block number")
			continue
		}

		log.Debug("non-identical block, performing write")
		written, werr := target.WriteAt(sourceBlock[:n], offset)
		if werr != nil {
			return stats, fmt.Errorf("write failed: %w", werr)
		}
		if written != n {
			return stats, fmt.Errorf("write failed: short write of %d bytes", written)
		}
		stats.Written++
		log.Debug("write complete, incrementing block number")
	}

	return stats, nil
}

// readBlock reads exactly one block at offset.  It returns blockSize on
// success, zero at a clean end of the volume, and errPartialBlock when
// the volume ends in the middle of a block (the C++ non-empty
// end-of-stream).
func readBlock(r io.ReaderAt, buf []byte, offset int64) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.ReadAt(buf[total:], offset+int64(total))
		total += n
		if err == io.EOF {
			if total == 0 {
				return 0, nil
			}
			return total, errPartialBlock
		}
		if err != nil {
			return total, fmt.Errorf("read failed: %w", err)
		}
		if n == 0 {
			// Defensive: a reader that returns no data and no
			// error cannot make progress.
			if total == 0 {
				return 0, nil
			}
			return total, errPartialBlock
		}
	}
	return total, nil
}

// reportReadError mirrors the C++ read_complete error handling: a
// non-empty end-of-stream additionally suggests a wrong block size.
func reportReadError(log *slog.Logger, err error, volume string) {
	if errors.Is(err, errPartialBlock) {
		log.Error("received non-empty end-of-stream", "volume", volume)
		log.Info("block size may be incorrect")
		return
	}
	log.Error("received error", "volume", volume, "error", err)
}
