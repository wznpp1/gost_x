package kcp

import (
	"net"
	"time"

	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	admission "github.com/wznpp1/gost_x/admission/wrapper"
	xnet "github.com/wznpp1/gost_x/internal/net"
	kcp_util "github.com/wznpp1/gost_x/internal/util/kcp"
	limiter "github.com/wznpp1/gost_x/limiter/traffic/wrapper"
	metrics "github.com/wznpp1/gost_x/metrics/wrapper"
	"github.com/wznpp1/gost_x/registry"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
	"github.com/xtaci/tcpraw"
)

func init() {
	registry.ListenerRegistry().Register("kcp", NewListener)
}

type kcpListener struct {
	conn    net.PacketConn
	ln      *kcp.Listener
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
	return &kcpListener{
		logger:  options.Logger,
		options: options,
	}
}

func (l *kcpListener) Init(md md.Metadata) (err error) {
	if err = l.parseMetadata(md); err != nil {
		return
	}

	config := l.md.config
	config.Init()

	var conn net.PacketConn
	if config.TCP {
		network := "tcp"
		if xnet.IsIPv4(l.options.Addr) {
			network = "tcp4"
		}
		conn, err = tcpraw.Listen(network, l.options.Addr)
	} else {
		network := "udp"
		if xnet.IsIPv4(l.options.Addr) {
			network = "udp4"
		}
		var udpAddr *net.UDPAddr
		udpAddr, err = net.ResolveUDPAddr(network, l.options.Addr)
		if err != nil {
			return
		}
		conn, err = net.ListenUDP(network, udpAddr)
	}
	if err != nil {
		return
	}

	conn = metrics.WrapUDPConn(l.options.Service, conn)
	conn = admission.WrapUDPConn(l.options.Admission, conn)
	conn = limiter.WrapUDPConn(l.options.TrafficLimiter, conn)

	ln, err := kcp.ServeConn(
		kcp_util.BlockCrypt(config.Key, config.Crypt, kcp_util.DefaultSalt),
		config.DataShard, config.ParityShard, conn)
	if err != nil {
		return
	}

	if config.DSCP > 0 {
		if er := ln.SetDSCP(config.DSCP); er != nil {
			l.logger.Warn(er)
		}
	}
	if er := ln.SetReadBuffer(config.SockBuf); er != nil {
		l.logger.Warn(er)
	}
	if er := ln.SetWriteBuffer(config.SockBuf); er != nil {
		l.logger.Warn(er)
	}

	l.ln = ln
	l.conn = conn
	l.cqueue = make(chan net.Conn, l.md.backlog)
	l.errChan = make(chan error, 1)

	go l.listenLoop()

	return
}

func (l *kcpListener) Accept() (conn net.Conn, err error) {
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

func (l *kcpListener) Close() error {
	l.conn.Close()
	return l.ln.Close()
}

func (l *kcpListener) Addr() net.Addr {
	return l.ln.Addr()
}

func (l *kcpListener) listenLoop() {
	for {
		conn, err := l.ln.AcceptKCP()
		if err != nil {
			l.logger.Error("accept:", err)
			l.errChan <- err
			close(l.errChan)
			return
		}

		conn.SetStreamMode(true)
		conn.SetWriteDelay(false)
		conn.SetNoDelay(
			l.md.config.NoDelay,
			l.md.config.Interval,
			l.md.config.Resend,
			l.md.config.NoCongestion,
		)
		conn.SetMtu(l.md.config.MTU)
		conn.SetWindowSize(l.md.config.SndWnd, l.md.config.RcvWnd)
		conn.SetACKNoDelay(l.md.config.AckNodelay)
		go l.mux(conn)
	}
}

func (l *kcpListener) mux(conn net.Conn) {
	defer conn.Close()

	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = l.md.config.SmuxVer
	smuxConfig.MaxReceiveBuffer = l.md.config.SmuxBuf
	smuxConfig.MaxStreamBuffer = l.md.config.StreamBuf
	smuxConfig.KeepAliveInterval = time.Duration(l.md.config.KeepAlive) * time.Second

	if !l.md.config.NoComp {
		conn = kcp_util.CompStreamConn(conn)
	}

	mux, err := smux.Server(conn, smuxConfig)
	if err != nil {
		l.logger.Error(err)
		return
	}
	defer mux.Close()

	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			l.logger.Error("accept stream: ", err)
			return
		}

		select {
		case l.cqueue <- stream:
		case <-stream.GetDieCh():
			stream.Close()
		default:
			stream.Close()
			l.logger.Warnf("connection queue is full, client %s discarded", stream.RemoteAddr())
		}
	}
}
