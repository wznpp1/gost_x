package grpc

import (
	"net"
	"time"

	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	admission "github.com/wznpp1/gost_x/admission/wrapper"
	xnet "github.com/wznpp1/gost_x/internal/net"
	"github.com/wznpp1/gost_x/internal/net/proxyproto"
	pb "github.com/wznpp1/gost_x/internal/util/grpc/proto"
	climiter "github.com/wznpp1/gost_x/limiter/conn/wrapper"
	limiter "github.com/wznpp1/gost_x/limiter/traffic/wrapper"
	metrics "github.com/wznpp1/gost_x/metrics/wrapper"
	"github.com/wznpp1/gost_x/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

func init() {
	registry.ListenerRegistry().Register("grpc", NewListener)
}

type grpcListener struct {
	addr    net.Addr
	server  *grpc.Server
	cqueue  chan net.Conn
	errChan chan error
	md      metadata
	logger  logger.Logger
	options listener.Options
}

func NewListener(opts ...listener.Option) listener.Listener {
	options := listener.Options{}
	for _, opt := range opts {
		opt(&options)
	}
	return &grpcListener{
		logger:  options.Logger,
		options: options,
	}
}

func (l *grpcListener) Init(md md.Metadata) (err error) {
	if err = l.parseMetadata(md); err != nil {
		return
	}

	network := "tcp"
	if xnet.IsIPv4(l.options.Addr) {
		network = "tcp4"
	}
	ln, err := net.Listen(network, l.options.Addr)
	if err != nil {
		return
	}
	ln = metrics.WrapListener(l.options.Service, ln)
	ln = proxyproto.WrapListener(l.options.ProxyProtocol, ln, 10*time.Second)
	ln = admission.WrapListener(l.options.Admission, ln)
	ln = limiter.WrapListener(l.options.TrafficLimiter, ln)
	ln = climiter.WrapListener(l.options.ConnLimiter, ln)

	var opts []grpc.ServerOption
	if !l.md.insecure {
		opts = append(opts, grpc.Creds(credentials.NewTLS(l.options.TLSConfig)))
	}
	if l.md.keepalive {
		opts = append(opts,
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime:             l.md.keepaliveMinTime,
				PermitWithoutStream: l.md.keepalivePermitWithoutStream,
			}),
			grpc.KeepaliveParams(keepalive.ServerParameters{
				MaxConnectionIdle: l.md.keepaliveMaxConnectionIdle,
				Time:              l.md.keepaliveTime,
				Timeout:           l.md.keepaliveTimeout,
			}),
		)
	}

	l.server = grpc.NewServer(opts...)
	l.addr = ln.Addr()
	l.cqueue = make(chan net.Conn, l.md.backlog)
	l.errChan = make(chan error, 1)

	pb.RegisterGostTunelServerX(l.server, &server{
		cqueue:    l.cqueue,
		localAddr: l.addr,
		logger:    l.options.Logger,
	}, l.md.path)

	go func() {
		err := l.server.Serve(ln)
		if err != nil {
			l.errChan <- err
		}
		close(l.errChan)
	}()

	return
}

func (l *grpcListener) Accept() (conn net.Conn, err error) {
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

func (l *grpcListener) Close() error {
	l.server.Stop()
	return nil
}

func (l *grpcListener) Addr() net.Addr {
	return l.addr
}
