package quic

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/quic-go/quic-go"
	quic_util "github.com/wznpp1/gost_x/internal/util/quic"
	"github.com/wznpp1/gost_x/registry"
)

func init() {
	registry.DialerRegistry().Register("quic", NewDialer)
}

type quicDialer struct {
	sessions     map[string]*quicSession
	sessionMutex sync.Mutex
	logger       logger.Logger
	md           metadata
	options      dialer.Options
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &quicDialer{
		sessions: make(map[string]*quicSession),
		logger:   options.Logger,
		options:  options,
	}
}

func (d *quicDialer) Init(md md.Metadata) (err error) {
	if err = d.parseMetadata(md); err != nil {
		return
	}

	return nil
}

func (d *quicDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (conn net.Conn, err error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "0")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}

	d.sessionMutex.Lock()
	defer d.sessionMutex.Unlock()

	session, ok := d.sessions[addr]
	if !ok {
		options := &dialer.DialOptions{}
		for _, opt := range opts {
			opt(options)
		}

		c, err := options.NetDialer.Dial(ctx, "udp", "")
		if err != nil {
			return nil, err
		}
		pc, ok := c.(net.PacketConn)
		if !ok {
			c.Close()
			return nil, errors.New("quic: wrong connection type")
		}

		if d.md.cipherKey != nil {
			pc = quic_util.CipherPacketConn(pc, d.md.cipherKey)
		}

		session, err = d.initSession(ctx, udpAddr, pc)
		if err != nil {
			d.logger.Error(err)
			pc.Close()
			return nil, err
		}

		d.sessions[addr] = session
	}

	conn, err = session.GetConn()
	if err != nil {
		session.Close()
		delete(d.sessions, addr)
		return nil, err
	}

	return
}

func (d *quicDialer) initSession(ctx context.Context, addr net.Addr, conn net.PacketConn) (*quicSession, error) {
	quicConfig := &quic.Config{
		KeepAlivePeriod:      d.md.keepAlivePeriod,
		HandshakeIdleTimeout: d.md.handshakeTimeout,
		MaxIdleTimeout:       d.md.maxIdleTimeout,
		Versions: []quic.VersionNumber{
			quic.Version1,
			quic.VersionDraft29,
		},
		MaxIncomingStreams: int64(d.md.maxStreams),
	}

	tlsCfg := d.options.TLSConfig
	tlsCfg.NextProtos = []string{"http/3", "quic/v1"}

	session, err := quic.DialEarlyContext(ctx, conn, addr, addr.String(), tlsCfg, quicConfig)
	if err != nil {
		return nil, err
	}
	return &quicSession{session: session}, nil
}

// Multiplex implements dialer.Multiplexer interface.
func (d *quicDialer) Multiplex() bool {
	return true
}
