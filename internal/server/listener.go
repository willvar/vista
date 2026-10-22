package server

import (
	"errors"
	"net"
	"sync"
)

// runtimeListener keeps accepted connections alive long enough to deliver a
// reset response while the restarted runtime begins listening on its new port.
type runtimeListener struct {
	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

func (l *runtimeListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		current, closed := l.listener, l.closed
		l.mu.Unlock()
		if closed {
			return nil, net.ErrClosed
		}
		conn, err := current.Accept()
		if !errors.Is(err, net.ErrClosed) {
			return conn, err
		}
		l.mu.Lock()
		replaced := l.listener != current
		l.mu.Unlock()
		if !replaced {
			return nil, err
		}
	}
}

func (l *runtimeListener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listener.Addr()
}

func (l *runtimeListener) replace(next net.Listener) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		next.Close()
		return net.ErrClosed
	}
	previous := l.listener
	l.listener = next
	return previous.Close()
}

func (l *runtimeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return l.listener.Close()
}
