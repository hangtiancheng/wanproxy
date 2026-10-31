// Command tack is the Go port of the C++ tack program: file-oriented
// dictionary compression using XCodec.
//
// Usage:
//
//	tack [-p cache | -F fifo-cache | -N] [-svQ] [-T [-ES]] -c [file ...]
//	tack [-p cache | -F fifo-cache | -N] [-svQ] [-T [-ES]] -d [file ...]
//	tack [-vQ] [-T [-ES]] -h [file ...]
//
// -c compresses, -d decompresses and -h prints the XCodec hash of every
// segment.  Cache selection: -p uses a persistent cache file (a
// concatenation of raw 2048-byte segments, like the C++
// TackPersistentCache), -F uses an XCDFS volume whose size is detected
// from the file, -N uses a null cache, and the default is a fresh
// in-memory cache.  -s prints byte statistics, -Q discards output, -v
// enables debug logging, -T times codec calls, -E times each file
// separately and -S prints per-call samples instead of summary stats.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

type fileAction int

const (
	actionNone fileAction = iota
	actionCompress
	actionDecompress
	actionHashes
)

const (
	flagQuietOutput   = 1 << iota // -Q
	flagCodecTiming               // -T
	flagByteStats                 // -s
	flagTimingEach                // -E
	flagTimingSamples             // -S
)

type options struct {
	action    fileAction
	persist   string
	fifo      string
	nullCache bool
	flags     uint
	verbose   bool
}

func (o options) has(f uint) bool { return o.flags&f != 0 }

var errUsage = fmt.Errorf("usage error")

func main() {
	opts, files := parseArgs(os.Args[1:])
	if err := run(opts, files, os.Stdin, os.Stdout, os.Stderr); err != nil {
		if err == errUsage {
			usage()
		}
		fmt.Fprintf(os.Stderr, "tack: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options, files []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	if opts.action == actionNone {
		return errUsage
	}
	if opts.action == actionHashes && (opts.has(flagByteStats) || opts.nullCache || opts.persist != "") {
		return errUsage
	}
	if opts.fifo != "" && (opts.persist != "" || opts.nullCache) {
		return errUsage
	}
	if opts.persist != "" && opts.nullCache {
		return errUsage
	}
	if !opts.has(flagCodecTiming) && (opts.has(flagTimingEach) || opts.has(flagTimingSamples)) {
		return errUsage
	}

	if opts.verbose {
		common.SetLevel(common.LevelDebug)
	} else {
		common.SetLevel(common.LevelInfo)
	}

	cache, closeCache, err := buildCache(opts)
	if err != nil {
		return err
	}
	defer closeCache()

	return processFiles(files, opts, cache, stdin, stdout, stderr)
}

func parseArgs(args []string) (options, []string) {
	var opts options
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
				case 'c':
					opts.action = actionCompress
				case 'd':
					opts.action = actionDecompress
				case 'h':
					opts.action = actionHashes
				case 'p':
					if j+1 < len(arg) {
						opts.persist = arg[j+1:]
						j = len(arg)
					} else if i+1 < len(args) {
						i++
						opts.persist = args[i]
					} else {
						usage()
					}
				case 'F':
					if j+1 < len(arg) {
						opts.fifo = arg[j+1:]
						j = len(arg)
					} else if i+1 < len(args) {
						i++
						opts.fifo = args[i]
					} else {
						usage()
					}
				case 'N':
					opts.nullCache = true
				case 's':
					opts.flags |= flagByteStats
				case 'v':
					opts.verbose = true
				case 'Q':
					opts.flags |= flagQuietOutput
				case 'T':
					opts.flags |= flagCodecTiming
				case 'E':
					opts.flags |= flagTimingEach
				case 'S':
					opts.flags |= flagTimingSamples
				case '?':
					usage()
				default:
					usage()
				}
			}
			continue
		}
		files = append(files, arg)
	}
	return opts, files
}

func usage() {
	fmt.Fprint(os.Stderr,
		"usage: tack [-p cache | -F fifo-cache | -N] [-svQ] [-T [-ES]] -c [file ...]\n"+
			"       tack [-p cache | -F fifo-cache | -N] [-svQ] [-T [-ES]] -d [file ...]\n"+
			"       tack [-vQ] [-T [-ES]] -h [file ...]\n")
	os.Exit(1)
}

func buildCache(opts options) (xcodec.Cache, func(), error) {
	switch {
	case opts.nullCache:
		return xcodec.NewNullCache(xcodec.CacheID{}), func() {}, nil

	case opts.fifo != "":
		disk, err := xcodec.OpenDisk(opts.fifo, 0, common.Logger())
		if err != nil {
			return nil, nil, fmt.Errorf("could not open on-disk FIFO cache: %w", err)
		}
		return disk.Local(), func() { disk.Close() }, nil

	case opts.persist != "":
		mode := os.O_RDWR | os.O_CREATE
		if opts.action == actionDecompress {
			// When decompressing, only allow reads.
			mode = os.O_RDONLY
		}
		f, err := os.OpenFile(opts.persist, mode, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("could not open persistent cache: %w", err)
		}
		cache, err := newPersistentCache(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return cache, func() { cache.Close(); f.Close() }, nil

	default:
		return xcodec.NewMemoryCache(), func() {}, nil
	}
}

// timer collects microsecond samples of codec call durations, mirroring
// the C++ common/timer Timer used by tack.
type timer struct {
	samples []uint64
	start   time.Time
	running bool
}

func (t *timer) begin() {
	t.start = time.Now()
	t.running = true
}

func (t *timer) stop() {
	if !t.running {
		return
	}
	t.running = false
	t.samples = append(t.samples, uint64(time.Since(t.start)/time.Microsecond))
}

func timeSamples(name string, t *timer, stderr io.Writer) {
	for _, sample := range t.samples {
		if name == "" {
			fmt.Fprintf(stderr, "%d\n", sample)
		} else {
			fmt.Fprintf(stderr, "%s,%d\n", name, sample)
		}
	}
}

func timeStats(name string, t *timer, stderr io.Writer) {
	var total uint64
	for _, sample := range t.samples {
		total += sample
	}
	fmt.Fprintf(stderr, "%s: %d timer samples.\n", name, len(t.samples))
	fmt.Fprintf(stderr, "%s: %d total runtime.\n", name, total)
	if len(t.samples) > 0 {
		fmt.Fprintf(stderr, "%s: %d mean microseconds/call.\n", name, total/uint64(len(t.samples)))
	}
}

func processFiles(files []string, opts options, cache xcodec.Cache, stdin io.Reader, stdout, stderr io.Writer) error {
	var t *timer
	if opts.has(flagCodecTiming) && !opts.has(flagTimingEach) {
		t = &timer{}
	}

	out := io.Writer(stdout)
	if opts.has(flagQuietOutput) {
		out = io.Discard
	}

	if len(files) == 0 {
		if err := processFile("<stdin>", stdin, out, stderr, opts, cache, t); err != nil {
			return err
		}
	} else {
		for _, file := range files {
			f, err := os.Open(file)
			if err != nil {
				fmt.Fprintf(stderr, "could not open: %s: %v\n", file, err)
				continue
			}
			err = processFile(file, f, out, stderr, opts, cache, t)
			f.Close()
			if err != nil {
				return err
			}
		}
	}

	if t != nil {
		if opts.has(flagTimingSamples) {
			timeSamples("", t, stderr)
		} else {
			timeStats("<total>", t, stderr)
		}
	}
	return nil
}

func processFile(name string, in io.Reader, out, stderr io.Writer, opts options, cache xcodec.Cache, shared *timer) error {
	t := shared
	if opts.has(flagCodecTiming) && opts.has(flagTimingEach) {
		t = &timer{}
	}

	var err error
	switch opts.action {
	case actionCompress:
		err = compress(name, in, out, stderr, cache, opts, t)
	case actionDecompress:
		err = decompress(name, in, out, stderr, cache, opts, t)
	case actionHashes:
		err = hashes(in, out, stderr, opts, t)
	}

	if t != nil && t != shared {
		if opts.has(flagTimingSamples) {
			timeSamples(name, t, stderr)
		} else {
			timeStats(name, t, stderr)
		}
	}
	return err
}

func compress(name string, in io.Reader, out, stderr io.Writer, cache xcodec.Cache, opts options, t *timer) error {
	encoder := xcodec.NewEncoder(cache)
	w := bufio.NewWriter(out)
	defer w.Flush()

	var inbytes, outbytes uint64
	buf := make([]byte, 65536)
	for {
		n, err := readChunk(in, buf)
		if n > 0 {
			if opts.has(flagByteStats) {
				inbytes += uint64(n)
			}
			var encoded []byte
			if t != nil {
				t.begin()
			}
			encoded = encoder.Encode(buf[:n])
			if t != nil {
				t.stop()
			}
			if opts.has(flagByteStats) {
				outbytes += uint64(len(encoded))
			}
			if _, werr := w.Write(encoded); werr != nil {
				return fmt.Errorf("write failed: %w", werr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}
	}
	if opts.has(flagByteStats) {
		printRatio(name, inbytes, outbytes, stderr)
	}
	return nil
}

func decompress(name string, in io.Reader, out, stderr io.Writer, cache xcodec.Cache, opts options, t *timer) error {
	decoder := xcodec.NewDecoder(cache)
	w := bufio.NewWriter(out)
	defer w.Flush()

	var inbytes, outbytes uint64
	buf := make([]byte, 65536)
	for {
		n, err := readChunk(in, buf)
		if n > 0 {
			if opts.has(flagByteStats) {
				inbytes += uint64(n)
			}
			var res xcodec.DecodeResult
			if t != nil {
				t.begin()
			}
			res = decoder.Decode(buf[:n])
			if t != nil {
				t.stop()
			}
			if res.Err != nil {
				return fmt.Errorf("decode failed: %w", res.Err)
			}
			if len(res.Unknown) > 0 {
				return fmt.Errorf("cannot decode stream with unknown hashes (%d)", len(res.Unknown))
			}
			if opts.has(flagByteStats) {
				outbytes += uint64(len(res.Output))
			}
			if _, werr := w.Write(res.Output); werr != nil {
				return fmt.Errorf("write failed: %w", werr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}
	}
	if err := decoder.Finish(); err != nil {
		return fmt.Errorf("incomplete stream: %w", err)
	}
	if opts.has(flagByteStats) {
		printRatio(name, outbytes, inbytes, stderr) // Reverse order of compress().
	}
	return nil
}

func hashes(in io.Reader, out, stderr io.Writer, opts options, t *timer) error {
	w := bufio.NewWriter(out)
	defer w.Flush()

	buf := make([]byte, 65536)
	var be [8]byte
	for {
		n, err := readChunk(in, buf)
		if n > 0 {
			if t != nil {
				t.begin()
			}
			h := xcodec.NewHash()
			fed := 0
			for i := 0; i < n; i++ {
				h.Add(buf[i])
				fed++
				if fed == xcodec.SegmentLength {
					binary.BigEndian.PutUint64(be[:], h.Mix())
					w.Write(be[:])
					h.Reset()
					fed = 0
				}
			}
			if t != nil {
				t.stop()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}
	}
	return nil
}

// readChunk reads at least one byte unless the stream is at EOF,
// mirroring the C++ fill() behaviour of returning whole reads.
func readChunk(r io.Reader, buf []byte) (int, error) {
	n, err := r.Read(buf)
	if n > 0 {
		return n, nil
	}
	return 0, err
}

func printRatio(name string, inbytes, outbytes uint64, stderr io.Writer) {
	fmt.Fprintf(stderr, "%s: %d uncompressed bytes, %d compressed bytes.\n", name, inbytes, outbytes)
	if outbytes == 0 {
		fmt.Fprintf(stderr, "%s: no compressed bytes.\n", name)
		return
	}
	if inbytes <= outbytes {
		fmt.Fprintf(stderr, "%s: bloat ratio 1:%f (%f:1)\n", name,
			float64(outbytes)/float64(inbytes), float64(inbytes)/float64(outbytes))
		return
	}
	fmt.Fprintf(stderr, "%s: compression ratio 1:%f (%f:1)\n", name,
		float64(outbytes)/float64(inbytes), float64(inbytes)/float64(outbytes))
}

// persistentCache mirrors the C++ TackPersistentCache: a memory cache
// warmed from a file of concatenated raw segments, appending newly
// entered segments and flushing them on Close.
type persistentCache struct {
	mem     *xcodec.MemoryCache
	f       *os.File
	newData []byte
}

func newPersistentCache(f *os.File) (*persistentCache, error) {
	c := &persistentCache{mem: xcodec.NewMemoryCache(), f: f}
	seg := make([]byte, xcodec.SegmentLength)
	for {
		n, err := io.ReadFull(f, seg)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n != 0 {
				return nil, fmt.Errorf("corrupt persistent cache")
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("corrupt persistent cache: %w", err)
		}
		hash := xcodec.HashSegment(seg)
		c.mem.Enter(hash, seg)
	}
	return c, nil
}

func (c *persistentCache) Close() {
	if len(c.newData) > 0 && c.f != nil {
		if _, err := c.f.Write(c.newData); err != nil {
			fmt.Fprintf(os.Stderr, "could not flush persistent cache: %v\n", err)
		}
		c.newData = nil
	}
}

func (c *persistentCache) Lookup(hash uint64) []byte     { return c.mem.Lookup(hash) }
func (c *persistentCache) Has(hash uint64) bool          { return c.mem.Has(hash) }
func (c *persistentCache) Size() int                     { return c.mem.Size() }
func (c *persistentCache) Touch(hash uint64, seg []byte) {}
func (c *persistentCache) OutOfBand() bool               { return true }
func (c *persistentCache) Connect(id xcodec.CacheID) xcodec.Cache {
	panic("tack: persistent cache does not support connect")
}
func (c *persistentCache) ID() xcodec.CacheID { return c.mem.ID() }

func (c *persistentCache) Enter(hash uint64, segment []byte) {
	c.mem.Enter(hash, segment)
	c.newData = append(c.newData, segment...)
}

func (c *persistentCache) Replace(hash uint64, segment []byte) {
	panic("tack: replace on persistent cache")
}
