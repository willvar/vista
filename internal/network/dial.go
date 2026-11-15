package network

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"golang.getoutline.org/sdk/transport"
)

// dialAddresses keeps resolution and policy in Vista while the library owns
// address scheduling, cancellation, and cleanup of unsuccessful attempts.
func (e *engine) dialAddresses(ctx context.Context, network, host, port string, addresses []string) (net.Conn, error) {
	if len(addresses) == 1 {
		return e.through(ctx, network, net.JoinHostPort(addresses[0], port))
	}
	ips, err := parseAddresses(addresses)
	if err != nil {
		return nil, err
	}
	return e.dialResolved(ctx, network, host, port, func(context.Context, string) <-chan transport.HappyEyeballsResolution {
		results := make(chan transport.HappyEyeballsResolution, 1)
		results <- transport.HappyEyeballsResolution{IPs: ips}
		close(results)
		return results
	})
}

func (e *engine) dialResolved(ctx context.Context, network, host, port string, resolve transport.HappyEyeballsResolveFunc) (net.Conn, error) {
	dialer := transport.HappyEyeballsStreamDialer{
		Resolve: resolve,
		Dialer: transport.FuncStreamDialer(func(ctx context.Context, address string) (transport.StreamConn, error) {
			conn, err := e.through(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return dialStreamConn{conn}, nil
		}),
	}
	// The scheduler skips Resolve for literal IPs. A local-only name ensures
	// even an IP authority uses exactly the supplied binding, without DNS.
	if net.ParseIP(host) != nil {
		host = "validated.invalid"
	}
	return dialer.DialStream(ctx, net.JoinHostPort(host, port))
}

func parseAddresses(addresses []string) ([]netip.Addr, error) {
	ips := make([]netip.Addr, len(addresses))
	for i, address := range addresses {
		var err error
		ips[i], err = netip.ParseAddr(address)
		if err != nil {
			return nil, err
		}
	}
	return ips, nil
}

// The scheduler uses only Close. Forward half-close when the underlying
// connection supports it, and report unsupported operations otherwise.
type dialStreamConn struct{ net.Conn }

func (c dialStreamConn) CloseRead() error {
	if conn, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return conn.CloseRead()
	}
	return errors.ErrUnsupported
}

func (c dialStreamConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return errors.ErrUnsupported
}
