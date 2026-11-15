package network

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
)

type boundTransportKey struct {
	engine          *engine
	host, addresses string
}

// Bindings have their own bounded pools so an old connection cannot bypass
// a new validation result.
var boundTransports = struct {
	sync.Mutex
	values map[boundTransportKey]*http.Transport
	order  []boundTransportKey
}{values: map[boundTransportKey]*http.Transport{}}

func (e *engine) boundTransport(binding addressSet) *http.Transport {
	addresses := slices.Clone(binding.addresses)
	slices.Sort(addresses)
	key := boundTransportKey{e, binding.host, strings.Join(slices.Compact(addresses), ",")}
	boundTransports.Lock()
	defer boundTransports.Unlock()
	if t := boundTransports.values[key]; t != nil {
		return t
	}
	t := e.base.Clone()
	t.Proxy = nil
	t.DialTLS, t.DialTLSContext = nil, nil
	// Transport may detach a dial from its originating request or give its
	// connection to another waiter. The entire pool uses this immutable binding.
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return e.dial(context.WithValue(ctx, addressKey{}, binding), network, address)
	}
	if len(boundTransports.order) >= 64 {
		old := boundTransports.order[0]
		boundTransports.order = boundTransports.order[1:]
		boundTransports.values[old].CloseIdleConnections()
		delete(boundTransports.values, old)
	}
	boundTransports.values[key] = t
	boundTransports.order = append(boundTransports.order, key)
	return t
}
