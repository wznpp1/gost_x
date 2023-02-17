package quic

import (
	"context"
	"net"

	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/quic-go/quic-go"
	admission "github.com/wznpp1/gost_x/admission/wrapper"
	icmp_pkg "github.com/wznpp1/gost_x/internal/util/icmp"
	limiter "github.com/wznpp1/gost_x/limiter/traffic/wrapper"
	metrics "github.com/wznpp1/gost_x/metrics/wrapper"
	"github.com/wznpp1/gost_x/registry"
	"golang.org/x/net/icmp"
)

func init() {
	registry.ListenerRegistry().Register("icmp", NewListener)
}

type icmpListener struct {
	ln      quic.EarlyListener
	cqueue  chan net.Conn
	errChan chan error
	logger  logger.Logger
	md      metadata
	options listener.Options
}

func NewListener(opts ...listener.Option) listener.Listener {
	options := listener.Options{}
	for _, opt := range opts {
		opt(&options)
	}
	return &icmpListener{
		logger:  options.Logger,
		options: options,
	}
}

func (l *icmpListener) Init(md md.Metadata) (err error) {
	if err = l.parseMetadata(md); err != nil {
		return
	}

	addr := l.options.Addr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}

	var conn net.PacketConn
	conn, err = icmp.ListenPacket("ip4:icmp", addr)
	if err != nil {
		return
	}
	conn = icmp_pkg.ServerConn(conn)
	conn = metrics.WrapPacketConn(l.options.Service, conn)
	conn = admission.WrapPacketConn(l.options.Admission, conn)
	conn = limiter.WrapPacketConn(l.options.TrafficLimiter, conn)

	config := &quic.Config{
		KeepAlivePeriod:      l.md.keepAlivePeriod,
		HandshakeIdleTimeout: l.md.handshakeTimeout,
		MaxIdleTimeout:       l.md.maxIdleTimeout,
		Versions: []quic.VersionNumber{
			quic.Version1,
			quic.VersionDraft29,
		},
	}

	tlsCfg := l.options.TLSConfig
	tlsCfg.NextProtos = []string{"http/3", "quic/v1"}

	ln, err := quic.ListenEarly(conn, tlsCfg, config)
	if err != nil {
		return
	}

	l.ln = ln
	l.cqueue = make(chan net.Conn, l.md.backlog)
	l.errChan = make(chan error, 1)

	go l.listenLoop()

	return
}

func (l *icmpListener) Accept() (conn net.Conn, err error) {
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

func (l *icmpListener) Close() error {
	return l.ln.Close()
}

func (l *icmpListener) Addr() net.Addr {
	return l.ln.Addr()
}

func (l *icmpListener) listenLoop() {
	for {
		ctx := context.Background()
		session, err := l.ln.Accept(ctx)
		if err != nil {
			l.logger.Error("accept: ", err)
			l.errChan <- err
			close(l.errChan)
			return
		}
		l.logger.Infof("new client session: %v", session.RemoteAddr())
		go l.mux(ctx, session)
	}
}

func (l *icmpListener) mux(ctx context.Context, session quic.EarlyConnection) {
	defer session.CloseWithError(0, "closed")

	for {
		stream, err := session.AcceptStream(ctx)
		if err != nil {
			l.logger.Error("accept stream: ", err)
			return
		}

		conn := &quicConn{
			Stream: stream,
			laddr:  session.LocalAddr(),
			raddr:  session.RemoteAddr(),
		}
		select {
		case l.cqueue <- conn:
		case <-stream.Context().Done():
			stream.Close()
		default:
			stream.Close()
			l.logger.Warnf("connection queue is full, client %s discarded", session.RemoteAddr())
		}
	}
}
