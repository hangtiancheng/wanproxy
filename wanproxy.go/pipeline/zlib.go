package pipeline

import (
	"compress/zlib"
	"io"
)

func NewDeflateWriter(w io.Writer, level int) (*zlib.Writer, error) {
	if level < zlib.HuffmanOnly || level > zlib.BestCompression {
		level = zlib.DefaultCompression
	}
	return zlib.NewWriterLevel(w, level)
}

func NewInflateReader(r io.Reader) (io.ReadCloser, error) {
	return zlib.NewReader(r)
}
