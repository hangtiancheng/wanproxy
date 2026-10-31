package pipeline

import (
	"compress/zlib"
	"io"
)

// NewDeflateWriter returns a writer that deflates everything written to
// it into w, mirroring the C++ DeflatePipe.  Close flushes the final
// compressed block; the underlying writer is not closed.
func NewDeflateWriter(w io.Writer, level int) (*zlib.Writer, error) {
	if level < zlib.HuffmanOnly || level > zlib.BestCompression {
		level = zlib.DefaultCompression
	}
	return zlib.NewWriterLevel(w, level)
}

// NewInflateReader returns a reader that inflates from r, mirroring the
// C++ InflatePipe.
func NewInflateReader(r io.Reader) (io.ReadCloser, error) {
	return zlib.NewReader(r)
}
