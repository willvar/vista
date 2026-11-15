package network

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
)

type stageError struct {
	stage string
	cause error
}

func (e *stageError) Error() string { return fmt.Sprintf("%s: %v", e.stage, e.cause) }
func (e *stageError) Unwrap() error { return e.cause }
func atStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &stageError{stage, err}
}
func FailureStage(err error) string {
	var staged *stageError
	if errors.As(err, &staged) {
		return staged.stage
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		// net/http marks failures before proxy negotiation, including the
		// proxy's native TLS handshake, with this typed operation identity.
		// Destination TLS after CONNECT is outside that wrapper.
		var operation *net.OpError
		if errors.As(err, &operation) && operation.Op == "proxyconnect" {
			return "proxy-tls"
		}
		return "target-tls"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	var operation *net.OpError
	if errors.As(err, &operation) {
		return "connection"
	}
	return "request"
}
