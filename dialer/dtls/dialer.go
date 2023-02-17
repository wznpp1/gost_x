package dtls

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/pion/dtls/v2"
	xdtls "github.com/wznpp1/gost_x/internal/util/dtls"
	"github.com/wznpp1/gost_x/registry"
)

func init() {
	registry.DialerRegistry().Register("dtls", NewDialer)
}

type dtlsDialer struct {
	md      metadata
	logger  logger.Logger
	options dialer.Options
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &dtlsDialer{
		logger:  options.Logger,
		options: options,
	}
}

func (d *dtlsDialer) Init(md md.Metadata) (err error) {
	return d.parseMetadata(md)
}

func (d *dtlsDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (net.Conn, error) {
	var options dialer.DialOptions
	for _, opt := range opts {
		opt(&options)
	}

	conn, err := options.NetDialer.Dial(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}

	tlsCfg := d.options.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
	config := dtls.Config{
		Certificates:         tlsCfg.Certificates,
		InsecureSkipVerify:   tlsCfg.InsecureSkipVerify,
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		ServerName:           tlsCfg.ServerName,
		RootCAs:              tlsCfg.RootCAs,
		FlightInterval:       d.md.flightInterval,
		MTU:                  d.md.mtu,
	}

	c, err := dtls.ClientWithContext(ctx, conn, &config)
	if err != nil {
		return nil, err
	}
	return xdtls.Conn(c, d.md.bufferSize), nil
}
