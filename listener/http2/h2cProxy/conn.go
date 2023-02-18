package h2cProxy

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// HTTP2 h2cProxyConnection, wrapped up just like a net.h2cProxyConn
type h2cProxyConn struct {
	r          io.Reader
	w          io.Writer
	remoteAddr net.Addr
	localAddr  net.Addr
	closed     chan struct{}
}

func (c *h2cProxyConn) Read(b []byte) (n int, err error) {
	return c.r.Read(b)
}

func (c *h2cProxyConn) Write(b []byte) (n int, err error) {
	return c.w.Write(b)
}

func (c *h2cProxyConn) Close() (err error) {
	select {
	case <-c.closed:
		return
	default:
		close(c.closed)
	}
	if rc, ok := c.r.(io.Closer); ok {
		err = rc.Close()
	}
	if w, ok := c.w.(io.Closer); ok {
		err = w.Close()
	}
	return
}

func (c *h2cProxyConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *h2cProxyConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *h2cProxyConn) SetDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "http2", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (c *h2cProxyConn) SetReadDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "http2", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (c *h2cProxyConn) SetWriteDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "http2", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

type flushWriter struct {
	w io.Writer
}

func (fw flushWriter) Write(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(string); ok {
				err = errors.New(s)
				// log.Log("[http2]", err)
				return
			}
			err = r.(error)
		}
	}()

	n, err = fw.w.Write(p)
	if err != nil {
		// log.Log("flush writer:", err)
		return
	}
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
	return
}
