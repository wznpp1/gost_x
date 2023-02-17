package tun

import (
	"bytes"
	"context"
	"io"
	"net"
	"time"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/core/logger"
	"github.com/songgao/water/waterutil"
	tun_util "github.com/wznpp1/gost_x/internal/util/tun"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	// 4-byte magic header followed by 16-byte key.
	keepAliveHeaderLength = 20
)

var (
	magicHeader = []byte("GOST")
)

func (h *tunHandler) handleClient(ctx context.Context, conn net.Conn, raddr string, config *tun_util.Config, log logger.Logger) error {
	var ips []net.IP
	for _, net := range config.Net {
		ips = append(ips, net.IP)
	}
	if len(ips) == 0 {
		return ErrInvalidNet
	}

	for {
		err := func() error {
			cc, err := h.router.Dial(ctx, "udp", raddr)
			if err != nil {
				return err
			}
			defer cc.Close()

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			go h.keepAlive(ctx, cc, ips)

			return h.transportClient(conn, cc, log)
		}()
		if err == ErrTun {
			return err
		}

		log.Error(err)
		time.Sleep(time.Second)
	}
}

func (h *tunHandler) keepAlive(ctx context.Context, conn net.Conn, ips []net.IP) {
	// handshake
	keepAliveData := bufpool.Get(keepAliveHeaderLength + len(ips)*net.IPv6len)
	defer bufpool.Put(keepAliveData)

	copy((*keepAliveData)[:4], magicHeader) // magic header
	copy((*keepAliveData)[4:20], []byte(h.md.passphrase))
	pos := 20
	for _, ip := range ips {
		copy((*keepAliveData)[pos:pos+net.IPv6len], ip.To16())
		pos += net.IPv6len
	}
	if _, err := conn.Write((*keepAliveData)); err != nil {
		return
	}

	if h.md.keepAlivePeriod <= 0 {
		return
	}
	conn.SetReadDeadline(time.Now().Add(h.md.keepAlivePeriod * 3))

	ticker := time.NewTicker(h.md.keepAlivePeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if _, err := conn.Write((*keepAliveData)); err != nil {
				return
			}
			h.options.Logger.Debugf("keepalive sended")
		case <-ctx.Done():
			return
		}
	}
}

func (h *tunHandler) transportClient(tun io.ReadWriter, conn net.Conn, log logger.Logger) error {
	errc := make(chan error, 1)

	go func() {
		for {
			err := func() error {
				b := bufpool.Get(h.md.bufferSize)
				defer bufpool.Put(b)

				n, err := tun.Read(*b)
				if err != nil {
					return ErrTun
				}

				if waterutil.IsIPv4((*b)[:n]) {
					header, err := ipv4.ParseHeader((*b)[:n])
					if err != nil {
						log.Warn(err)
						return nil
					}

					log.Tracef("%s >> %s %-4s %d/%-4d %-4x %d",
						header.Src, header.Dst, ipProtocol(waterutil.IPv4Protocol((*b)[:n])),
						header.Len, header.TotalLen, header.ID, header.Flags)
				} else if waterutil.IsIPv6((*b)[:n]) {
					header, err := ipv6.ParseHeader((*b)[:n])
					if err != nil {
						log.Warn(err)
						return nil
					}

					log.Tracef("%s >> %s %s %d %d",
						header.Src, header.Dst,
						ipProtocol(waterutil.IPProtocol(header.NextHeader)),
						header.PayloadLen, header.TrafficClass)
				} else {
					log.Warn("unknown packet, discarded")
					return nil
				}

				_, err = conn.Write((*b)[:n])
				return err
			}()

			if err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() {
		for {
			err := func() error {
				b := bufpool.Get(h.md.bufferSize)
				defer bufpool.Put(b)

				n, err := conn.Read(*b)
				if err != nil {
					return err
				}

				if n == keepAliveHeaderLength && bytes.Equal((*b)[:4], magicHeader) {
					ip := net.IP((*b)[4:20])
					log.Debugf("keepalive received at %v", ip)

					if h.md.keepAlivePeriod > 0 {
						conn.SetReadDeadline(time.Now().Add(h.md.keepAlivePeriod * 3))
					}
					return nil
				}

				if waterutil.IsIPv4((*b)[:n]) {
					header, err := ipv4.ParseHeader((*b)[:n])
					if err != nil {
						log.Warn(err)
						return nil
					}

					log.Tracef("%s >> %s %-4s %d/%-4d %-4x %d",
						header.Src, header.Dst, ipProtocol(waterutil.IPv4Protocol((*b)[:n])),
						header.Len, header.TotalLen, header.ID, header.Flags)
				} else if waterutil.IsIPv6((*b)[:n]) {
					header, err := ipv6.ParseHeader((*b)[:n])
					if err != nil {
						log.Warn(err)
						return nil
					}

					log.Tracef("%s > %s %s %d %d",
						header.Src, header.Dst,
						ipProtocol(waterutil.IPProtocol(header.NextHeader)),
						header.PayloadLen, header.TrafficClass)
				} else {
					log.Warn("unknown packet, discarded")
					return nil
				}

				if _, err = tun.Write((*b)[:n]); err != nil {
					return ErrTun
				}
				return nil
			}()

			if err != nil {
				errc <- err
				return
			}
		}
	}()

	err := <-errc
	if err != nil && err == io.EOF {
		err = nil
	}
	return err
}
