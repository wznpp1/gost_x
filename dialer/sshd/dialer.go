package sshd

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/go-gost/core/dialer"
	md "github.com/go-gost/core/metadata"
	ssh_util "github.com/wznpp1/gost_x/internal/util/ssh"
	"github.com/wznpp1/gost_x/registry"
	"golang.org/x/crypto/ssh"
)

func init() {
	registry.DialerRegistry().Register("sshd", NewDialer)
}

type sshdDialer struct {
	sessions     map[string]*sshSession
	sessionMutex sync.Mutex
	md           metadata
	options      dialer.Options
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &sshdDialer{
		sessions: make(map[string]*sshSession),
		options:  options,
	}
}

func (d *sshdDialer) Init(md md.Metadata) (err error) {
	if err = d.parseMetadata(md); err != nil {
		return
	}

	return nil
}

// Multiplex implements dialer.Multiplexer interface.
func (d *sshdDialer) Multiplex() bool {
	return true
}

func (d *sshdDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (conn net.Conn, err error) {
	d.sessionMutex.Lock()
	defer d.sessionMutex.Unlock()

	session, ok := d.sessions[addr]
	if session != nil && session.IsClosed() {
		delete(d.sessions, addr) // session is dead
		ok = false
	}
	if !ok {
		var options dialer.DialOptions
		for _, opt := range opts {
			opt(&options)
		}

		conn, err = options.NetDialer.Dial(ctx, "tcp", addr)
		if err != nil {
			return
		}

		session = &sshSession{
			addr: addr,
			conn: conn,
		}
		d.sessions[addr] = session
	}

	return session.conn, err
}

// Handshake implements dialer.Handshaker
func (d *sshdDialer) Handshake(ctx context.Context, conn net.Conn, options ...dialer.HandshakeOption) (net.Conn, error) {
	opts := &dialer.HandshakeOptions{}
	for _, option := range options {
		option(opts)
	}

	d.sessionMutex.Lock()
	defer d.sessionMutex.Unlock()

	if d.md.handshakeTimeout > 0 {
		conn.SetDeadline(time.Now().Add(d.md.handshakeTimeout))
		defer conn.SetDeadline(time.Time{})
	}

	log := d.options.Logger

	session, ok := d.sessions[opts.Addr]
	if session != nil && session.conn != conn {
		err := errors.New("ssh: unrecognized connection")
		log.Error(err)
		conn.Close()
		delete(d.sessions, opts.Addr)
		return nil, err
	}

	if !ok || session.client == nil {
		s, err := d.initSession(ctx, opts.Addr, conn)
		if err != nil {
			log.Error(err)
			conn.Close()
			delete(d.sessions, opts.Addr)
			return nil, err
		}
		session = s
		go func() {
			s.wait()
			log.Debug("session closed")
		}()
		d.sessions[opts.Addr] = session
	}
	if session.IsClosed() {
		delete(d.sessions, opts.Addr)
		return nil, ssh_util.ErrSessionDead
	}

	return ssh_util.NewClientConn(session.conn, session.client), nil
}

func (d *sshdDialer) initSession(ctx context.Context, addr string, conn net.Conn) (*sshSession, error) {
	config := ssh.ClientConfig{
		// Timeout:         timeout,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	if d.options.Auth != nil {
		config.User = d.options.Auth.Username()
		if password, _ := d.options.Auth.Password(); password != "" {
			config.Auth = []ssh.AuthMethod{
				ssh.Password(password),
			}
		}
	}
	if d.md.signer != nil {
		config.Auth = append(config.Auth, ssh.PublicKeys(d.md.signer))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, &config)
	if err != nil {
		return nil, err
	}

	return &sshSession{
		conn:   conn,
		client: ssh.NewClient(sshConn, chans, reqs),
		closed: make(chan struct{}),
		dead:   make(chan struct{}),
	}, nil
}
