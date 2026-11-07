package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

var dumpVerbosity int

func main() {
	args := os.Args[1:]
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case arg == "-?" || arg == "-h" || arg == "--help":
			usage()
		case strings.HasPrefix(arg, "-") && arg != "-":
			for _, c := range arg[1:] {
				if c != 'v' {
					usage()
				}
				dumpVerbosity++
			}
		default:
			files = append(files, arg)
		}
	}

	if len(files) == 0 {
		dump(os.Stdin, os.Stdout)
		return
	}
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not open %s: %v\n", file, err)
			os.Exit(1)
		}
		dump(f, os.Stdout)
		f.Close()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: xcdump [-v] [file ...]")
	os.Exit(1)
}

func dump(in io.Reader, out io.Writer) {
	w := bufio.NewWriter(out)
	defer w.Flush()

	var buf []byte
	reader := bufio.NewReader(in)

	for {
		chunk := make([]byte, 65536)
		n, err := reader.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			buf = dumpFrames(w, buf)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "read failed: %v\n", err)
			return
		}
	}
	if len(buf) > 0 {
		printData(w, buf)
	}
}

func dumpFrames(w *bufio.Writer, buf []byte) []byte {
	for len(buf) > 0 {
		off := -1
		for i := range buf {
			if buf[i] == xcodec.Magic {
				off = i
				break
			}
		}
		if off < 0 {
			printData(w, buf)
			return nil
		}
		if off > 0 {
			printData(w, buf[:off])
			buf = buf[off:]
		}
		if len(buf) == 1 {
			return buf
		}

		frame, consumed, status, err := xcodec.ReadFrame(buf)
		if status == xcodec.FrameReadNeedMore {
			return buf
		}
		if status == xcodec.FrameReadInvalid {
			fmt.Fprintf(os.Stderr, "dump error: %v\n", err)
			return nil
		}
		buf = buf[consumed:]

		switch frame.Kind {
		case xcodec.FrameEscape:
			fmt.Fprintln(w, "<escape />")
		case xcodec.FrameExtract:
			hash := xcodec.HashSegment(frame.Segment)
			line := "<hash-declare"
			if dumpVerbosity > 0 {
				line += fmt.Sprintf(" hash=\"0x%016x\"", hash)
				if dumpVerbosity > 1 {
					line += " data=\"" + hex.EncodeToString(frame.Segment) + "\""
				}
			}
			fmt.Fprintln(w, line+"/>")
		case xcodec.FrameRef:
			line := "<hash-reference"
			if dumpVerbosity > 0 {
				line += fmt.Sprintf(" hash=\"0x%016x\"", frame.Hash)
			}
			fmt.Fprintln(w, line+"/>")
		case xcodec.FrameBackref:
			line := "<back-reference"
			if dumpVerbosity > 0 {
				line += fmt.Sprintf(" offset=\"%d\"", frame.Index)
			}
			fmt.Fprintln(w, line+"/>")
		}
	}
	return buf
}

func printData(w *bufio.Writer, data []byte) {
	line := "<data"
	if dumpVerbosity > 1 {
		line += " data=\"" + hex.EncodeToString(data) + "\""
	}
	fmt.Fprintln(w, line+"/>")
}
