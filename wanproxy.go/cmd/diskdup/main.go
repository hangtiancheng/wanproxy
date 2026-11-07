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

	common.ResetMasks()
	log := common.Component("/diskdup")

	_, err = dupBlocks(log, source, target, blockSize)
	return err
}

type Stats struct {
	Compared int64

	Written int64
}

type blockVolume interface {
	io.ReaderAt
	io.WriterAt
}

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
			if total == 0 {
				return 0, nil
			}
			return total, errPartialBlock
		}
	}
	return total, nil
}

func reportReadError(log *slog.Logger, err error, volume string) {
	if errors.Is(err, errPartialBlock) {
		log.Error("received non-empty end-of-stream", "volume", volume)
		log.Info("block size may be incorrect")
		return
	}
	log.Error("received error", "volume", volume, "error", err)
}
