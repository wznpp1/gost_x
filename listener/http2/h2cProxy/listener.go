package h2cProxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/cssivision/reverseproxy"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	admission "github.com/wznpp1/gost_x/admission/wrapper"
	xnet "github.com/wznpp1/gost_x/internal/net"
	"github.com/wznpp1/gost_x/internal/net/proxyproto"
	climiter "github.com/wznpp1/gost_x/limiter/conn/wrapper"
	limiter "github.com/wznpp1/gost_x/limiter/traffic/wrapper"
	metrics "github.com/wznpp1/gost_x/metrics/wrapper"
	"github.com/wznpp1/gost_x/registry"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func init() {
	registry.ListenerRegistry().Register("h2cProxy", NewListener)
}

type h2cProxyListener struct {
	server  *http.Server
	addr    net.Addr
	cqueue  chan net.Conn
	errChan chan error
	logger  logger.Logger
	md      metadata
	h2c     bool
	options listener.Options
}

func NewListener(opts ...listener.Option) listener.Listener {
	options := listener.Options{}
	for _, opt := range opts {
		opt(&options)
	}
	return &h2cProxyListener{
		h2c:     true,
		logger:  options.Logger,
		options: options,
	}
}

func NewTLSListener(opts ...listener.Option) listener.Listener {
	options := listener.Options{}
	for _, opt := range opts {
		opt(&options)
	}
	return &h2cProxyListener{
		logger:  options.Logger,
		options: options,
	}
}

func (l *h2cProxyListener) Init(md md.Metadata) (err error) {
	if err = l.parseMetadata(md); err != nil {
		return
	}

	l.server = &http.Server{
		Addr: l.options.Addr,
	}

	network := "tcp"
	if xnet.IsIPv4(l.options.Addr) {
		network = "tcp4"
	}
	ln, err := net.Listen(network, l.options.Addr)
	if err != nil {
		return err
	}

	l.addr = ln.Addr()
	ln = metrics.WrapListener(l.options.Service, ln)
	ln = proxyproto.WrapListener(l.options.ProxyProtocol, ln, 10*time.Second)
	ln = admission.WrapListener(l.options.Admission, ln)
	ln = limiter.WrapListener(l.options.TrafficLimiter, ln)
	ln = climiter.WrapListener(l.options.ConnLimiter, ln)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello h2c")
	})

	mux.HandleFunc("/proxy/*", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello proxy")
	})

	mux.HandleFunc("/http2", l.handleFunc)

	if l.h2c {
		l.server.Handler = h2c.NewHandler(mux, &http2.Server{})
	} else {
		l.server.Handler = http.HandlerFunc(l.handleFunc)
		l.server.TLSConfig = l.options.TLSConfig
		if err := http2.ConfigureServer(l.server, nil); err != nil {
			ln.Close()
			return err
		}
		ln = tls.NewListener(ln, l.options.TLSConfig)
	}

	l.cqueue = make(chan net.Conn, l.md.backlog)
	l.errChan = make(chan error, 1)

	go func() {
		if err := l.server.Serve(ln); err != nil {
			l.logger.Error(err)
		}
	}()

	return
}

func (l *h2cProxyListener) Accept() (conn net.Conn, err error) {
	var ok bool
	select {
	case conn = <-l.cqueue:
	case err, ok = <-l.errChan:
		if !ok {
			err = listener.ErrClosed
		}
	}
	return
}

func (l *h2cProxyListener) Addr() net.Addr {
	return l.addr
}

func (l *h2cProxyListener) Close() (err error) {
	select {
	case <-l.errChan:
	default:
		err = l.server.Close()
		l.errChan <- err
		close(l.errChan)
	}
	return nil
}

func (l *h2cProxyListener) handleFunc(w http.ResponseWriter, r *http.Request) {

	if l.logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(r, false)
		l.logger.Trace(string(dump))
	}

	if r.RequestURI == "/proxy" {
		path, err := url.Parse("http://127.0.0.1:8899/_/")
		if err != nil {
			panic(err)
		}
		proxy := reverseproxy.NewReverseProxy(path)
		proxy.ServeHTTP(w, r)
		return
	}

	if find := strings.Contains(r.RequestURI, "/proxy"); find {
		RequestURI := r.RequestURI
		r.RequestURI = strings.Replace(RequestURI, "k", "ky", 2)

		path, err := url.Parse("http://127.0.0.1:8899/_/")
		if err != nil {
			panic(err)
		}
		proxy := reverseproxy.NewReverseProxy(path)
		proxy.ServeHTTP(w, r)
		return
	}

	conn, err := l.upgrade(w, r)
	if err != nil {
		l.logger.Error(err)
		return
	}
	select {
	case l.cqueue <- conn:
	default:
		conn.Close()
		l.logger.Warnf("connection queue is full, client %s discarded", r.RemoteAddr)
	}

	<-conn.closed // NOTE: we need to wait for streaming end, or the connection will be closed
}

func (l *h2cProxyListener) upgrade(w http.ResponseWriter, r *http.Request) (*h2cProxyConn, error) {

	if l.md.path == "" && r.Method != http.MethodConnect {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil, errors.New("method not allowed")
	}

	if l.md.path != "" && r.RequestURI != l.md.path {
		w.WriteHeader(http.StatusBadRequest)
		return nil, errors.New("bad request")
	}

	w.WriteHeader(http.StatusOK)
	if fw, ok := w.(http.Flusher); ok {
		fw.Flush() // write header to client
	}

	remoteAddr, _ := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if remoteAddr == nil {
		remoteAddr = &net.TCPAddr{
			IP:   net.IPv4zero,
			Port: 0,
		}
	}
	return &h2cProxyConn{
		r:          r.Body,
		w:          flushWriter{w},
		localAddr:  l.addr,
		remoteAddr: remoteAddr,
		closed:     make(chan struct{}),
	}, nil
}
