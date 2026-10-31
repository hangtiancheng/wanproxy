package proxy

import (
	"compress/zlib"
	"errors"
	"io"
	"sync"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
)

// errWireClosed is returned by writes after the wire write side has been
// half-closed.
var errWireClosed = errors.New("proxy: wire write side is closed")

// wireWriter is the synchronized write side toward one peer.  It carries
// everything the local session produces toward that peer: encoded data
// frames as well as the crossed-over protocol responses (<ASK>, <LEARN>,
// <ADVANCE>, <EOS_ACK>), optionally deflated as a single stream, exactly
// like the C++ pipe chain feeding one socket.
type wireWriter struct {
	mu          sync.Mutex
	conn        StreamConn
	sink        io.Writer // conn, optionally counting raw socket bytes
	zw          *zlib.Writer
	writeClosed bool
}

// countingWriter counts raw socket bytes, mirroring the C++ PipeByteCount
// stages that sit closest to the socket.
type countingWriter struct {
	w     io.Writer
	count func(n int)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 && c.count != nil {
		c.count(n)
	}
	return n, err
}

func newWireWriter(conn StreamConn, deflate bool, level int, count func(n int)) (*wireWriter, error) {
	w := &wireWriter{conn: conn}
	w.sink = &countingWriter{w: conn, count: count}
	if deflate {
		zw, err := pipeline.NewDeflateWriter(w.sink, level)
		if err != nil {
			return nil, err
		}
		w.zw = zw
	}
	return w, nil
}

// Write sends session bytes to the peer, deflating when configured.
// Like the C++ DeflatePipe, which ends each consume with Z_SYNC_FLUSH so
// that data reaches the wire immediately, every write is flushed.
func (w *wireWriter) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeClosed {
		return errWireClosed
	}
	if w.zw != nil {
		if _, err := w.zw.Write(p); err != nil {
			return err
		}
		return w.zw.Flush()
	}
	_, err := w.sink.Write(p)
	return err
}

// Close flushes any deflate state and half-closes the socket write side,
// mirroring the C++ encoder channel producing EOS into the deflate pipe.
func (w *wireWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeClosed {
		return nil
	}
	w.writeClosed = true
	if w.zw != nil {
		_ = w.zw.Close()
	}
	return closeWrite(w.conn)
}

func closeWrite(conn StreamConn) error {
	if hc, ok := conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return nil
}

// countingReader counts raw socket reads, mirroring the C++
// PipeByteCount stage at the head of each splice.
type countingReader struct {
	r     io.Reader
	count func(n int)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.count != nil {
		c.count(n)
	}
	return n, err
}
