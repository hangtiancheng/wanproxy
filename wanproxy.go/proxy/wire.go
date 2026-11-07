package proxy

import (
	"compress/zlib"
	"errors"
	"io"
	"sync"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
)

var errWireClosed = errors.New("proxy: wire write side is closed")

type wireWriter struct {
	mu          sync.Mutex
	conn        StreamConn
	sink        io.Writer
	zw          *zlib.Writer
	writeClosed bool
}

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
	return writeFull(w.sink, p)
}

func (w *wireWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeClosed {
		return nil
	}
	w.writeClosed = true
	var err error
	if w.zw != nil {
		err = w.zw.Close()
	}
	return errors.Join(err, closeWrite(w.conn))
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func closeWrite(conn StreamConn) error {
	if hc, ok := conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return nil
}

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
