package forward

import (
	"context"
	"net"

	"github.com/go-gost/core/connector"
	md "github.com/go-gost/core/metadata"
	"github.com/wznpp1/gost_x/registry"
)

func init() {
	registry.ConnectorRegistry().Register("forward", NewConnector)
}

type forwardConnector struct {
	options connector.Options
}

func NewConnector(opts ...connector.Option) connector.Connector {
	options := connector.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &forwardConnector{
		options: options,
	}
}

func (c *forwardConnector) Init(md md.Metadata) (err error) {
	return nil
}

func (c *forwardConnector) Connect(ctx context.Context, conn net.Conn, network, address string, opts ...connector.ConnectOption) (net.Conn, error) {
	log := c.options.Logger.WithFields(map[string]any{
		"remote":  conn.RemoteAddr().String(),
		"local":   conn.LocalAddr().String(),
		"network": network,
		"address": address,
	})
	log.Debugf("connect %s/%s", address, network)

	return conn, nil
}
