package http2

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/wznpp1/gost_x/registry"
)

func init() {
	registry.ConnectorRegistry().Register("http2", NewConnector)
}

type http2Connector struct {
	md      metadata
	options connector.Options
}

func NewConnector(opts ...connector.Option) connector.Connector {
	options := connector.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &http2Connector{
		options: options,
	}
}

func (c *http2Connector) Init(md md.Metadata) (err error) {
	return c.parseMetadata(md)
}

func (c *http2Connector) Connect(ctx context.Context, conn net.Conn, network, address string, opts ...connector.ConnectOption) (net.Conn, error) {
	log := c.options.Logger.WithFields(map[string]any{
		"local":   conn.LocalAddr().String(),
		"remote":  conn.RemoteAddr().String(),
		"network": network,
		"address": address,
	})
	log.Debugf("connect %s/%s", address, network)

	v, _ := conn.(md.Metadatable)
	if v == nil {
		err := errors.New("http2: wrong connection type")
		log.Error(err)
		return nil, err
	}

	pr, pw := io.Pipe()
	req := &http.Request{
		Method:     http.MethodConnect,
		URL:        &url.URL{Scheme: "https", Host: conn.RemoteAddr().String()},
		Host:       address,
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header:     c.md.header,
		Body:       pr,
		// ContentLength: -1,
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	if user := c.options.Auth; user != nil {
		u := user.Username()
		p, _ := user.Password()
		req.Header.Set("Proxy-Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(u+":"+p)))
	}

	if log.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(req, false)
		log.Trace(string(dump))
	}

	if c.md.connectTimeout > 0 {
		conn.SetDeadline(time.Now().Add(c.md.connectTimeout))
		defer conn.SetDeadline(time.Time{})
	}

	client := v.Metadata().Get("client").(*http.Client)
	resp, err := client.Do(req)
	if err != nil {
		log.Error(err)
		conn.Close()
		return nil, err
	}

	if log.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpResponse(resp, false)
		log.Trace(string(dump))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		err = fmt.Errorf("%s", resp.Status)
		log.Error(err)
		return nil, err
	}

	hc := &http2Conn{
		r:         resp.Body,
		w:         pw,
		localAddr: conn.RemoteAddr(),
	}

	hc.remoteAddr, _ = net.ResolveTCPAddr(network, address)

	return hc, nil
}
