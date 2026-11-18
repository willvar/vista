package provider

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
	"vista/internal/network"
)

type callUsageKey struct{}

func WithCallUsageRecorder(ctx context.Context, record func(modelcfg.CallUsage)) context.Context {
	return context.WithValue(ctx, callUsageKey{}, record)
}

func measureModelTurn(ctx context.Context, m modelcfg.Model, onText, onReasoning func(string) error, run func(context.Context, func(string) error, func(string) error) (Turn, error)) (Turn, error) {
	started := time.Now()
	var first *float64
	wrap := func(next func(string) error) func(string) error {
		return func(text string) error {
			if first == nil && text != "" {
				first = new(time.Since(started).Seconds() * 1000)
			}
			if next != nil {
				return next(text)
			}
			return nil
		}
	}
	record, _ := ctx.Value(callUsageKey{}).(func(modelcfg.CallUsage))
	connection := m.UsageConnection()
	var netTiming connectionTiming
	if record != nil && connection != "" {
		ctx = httptrace.WithClientTrace(ctx, netTiming.trace())
	}
	// Keep the provider's explicit reasoning-sink contract. A nil sink must
	// still reject a reasoning stream rather than silently consuming it.
	if onReasoning != nil {
		onReasoning = wrap(onReasoning)
	}
	turn, err := run(ctx, wrap(onText), onReasoning)
	turn.FirstTokenMS, turn.DurationMS = first, time.Since(started).Seconds()*1000
	turn.ActualProcessingTier = normalizeProcessingTier(m, turn.ReportedProcessingTier)
	if record != nil && connection != "" {
		policy, _ := network.ConnectionPolicy(ctx)
		proxyID := ""
		if policy.Profile != nil {
			profile := *policy.Profile
			proxyID = profile.ID
			// Hash only non-secret route identity, not proxy authentication.
			profile.Username, profile.Password = "", ""
			policy.Profile = &profile
		}
		route := sha256.Sum256([]byte(network.Fingerprint(policy) + "/" + ModelEndpointID(m)))
		netTiming.mu.Lock()
		connected := netTiming.connected
		entry := modelcfg.CallUsage{ID: uuid.NewString(), Connection: connection, Model: m.ID, ProxyID: proxyID, Route: hex.EncodeToString(route[:]), RequestedTier: m.ProcessingTier, ActualTier: turn.ActualProcessingTier, Outcome: callOutcome(err, connected), FirstTokenMS: first}
		entry.ConnectMS, entry.TLSMS = netTiming.connect, netTiming.tls
		netTiming.mu.Unlock()
		if err == nil && turn.Usage != nil && turn.Usage.OutputTokens > 0 && turn.DurationMS > 0 {
			entry.OutputTPS = new(float64(turn.Usage.OutputTokens) * 1000 / turn.DurationMS)
		}
		record(entry)
	}
	return turn, err
}

func callOutcome(err error, connected bool) string {
	if err == nil {
		return "succeeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var httpErr *ModelHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == 429 || strings.Contains(strings.ToLower(httpErr.Code), "rate_limit") || httpErr.StatusCode == 498 {
			return "limited"
		}
		if httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 {
			return "rejected"
		}
		return "upstream"
	}
	var netErr net.Error
	if !connected && errors.As(err, &netErr) {
		return "connection"
	}
	return "failed"
}

// Measure from requesting a connection to a fresh one being ready, including
// egress proxy negotiation. A reused connection isn't a zero-latency dial.
type connectionTiming struct {
	mu                       sync.Mutex
	start, tlsStart          time.Time
	connect, tls, pendingTLS *float64
	connected                bool
}

func (t *connectionTiming) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.start = time.Now()
			t.connected = false
			t.pendingTLS = nil
		},
		TLSHandshakeStart: func() { t.mu.Lock(); defer t.mu.Unlock(); t.tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if err == nil && !t.tlsStart.IsZero() {
				t.pendingTLS = new(time.Since(t.tlsStart).Seconds() * 1000)
			}
			t.tlsStart = time.Time{}
		},
		GotConn: func(i httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.connected = true
			if !i.Reused && !t.start.IsZero() {
				t.connect = new(time.Since(t.start).Seconds() * 1000)
				t.tls = t.pendingTLS
			}
		},
	}
}
