package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

type addressKey struct{}
type addressSet struct {
	host      string
	addresses []string
}

func addressHost(host string) (string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String(), nil
	}
	host, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", errors.New("invalid address binding hostname")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", errors.New("invalid address binding hostname")
	}
	return host, nil
}

// WithAddresses binds a validated lookup to the actual connection. Original
// HTTP/TLS identities remain unchanged; a redirect needs its own validation.
func WithAddresses(ctx context.Context, host string, addresses []net.IP) (context.Context, error) {
	host, err := addressHost(host)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(addresses))
	for _, ip := range addresses {
		if ip != nil {
			values = append(values, ip.String())
		}
	}
	if len(values) == 0 {
		return nil, errors.New("address binding requires at least one address")
	}
	return context.WithValue(ctx, addressKey{}, addressSet{host, values}), nil
}
func pinnedAddresses(ctx context.Context, host string) ([]string, bool, error) {
	set, ok := ctx.Value(addressKey{}).(addressSet)
	if !ok {
		return nil, false, nil
	}
	host, err := addressHost(host)
	if err != nil {
		return nil, false, err
	}
	return set.addresses, set.host == host && len(set.addresses) > 0, nil
}

func loopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func loopbackAddresses() []string { return []string{"::1", "127.0.0.1"} }

// LookupURL applies the effective route for the original authority once.
func LookupURL(ctx context.Context, u *url.URL) (LookupResult, error) {
	policy, err := FromContext(ctx)
	if err != nil {
		return LookupResult{}, err
	}
	return Lookup(WithPolicy(ctx, policy.Effective(u)), u.Hostname())
}
