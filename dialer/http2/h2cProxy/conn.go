package h2cProxy

import (
	"errors"
	"io"
	"net"
	"time"
)

// HTTP2 connection, wrapped up just like a net.Conn.
type h2cProxyhttp2Conn struct {
	r          io.Reader
	w          io.Writer
	remoteAddr net.Addr
	localAddr  net.Addr
}

func (c *h2cProxyhttp2Conn) Read(b []byte) (n int, err error) {
	return c.r.Read(b)
}

func (c *h2cProxyhttp2Conn) Write(b []byte) (n int, err error) {
	return c.w.Write(b)
}

func (c *h2cProxyhttp2Conn) Close() (err error) {
	if r, ok := c.r.(io.Closer); ok {
		err = r.Close()
	}
	if w, ok := c.w.(io.Closer); ok {
		err = w.Close()
	}
	return
}

func (c *h2cProxyhttp2Conn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *h2cProxyhttp2Conn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *h2cProxyhttp2Conn) SetDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "h2", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (c *h2cProxyhttp2Conn) SetReadDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "h2", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (c *h2cProxyhttp2Conn) SetWriteDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "h2", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}
