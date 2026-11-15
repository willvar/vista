// Package network owns explicit application egress policies. It never reads
// proxy environment variables or changes the environment inherited by tools.
package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"

	"vista/internal/configstore"
)

type Profile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Protocol           string `json:"protocol"`
	Server             string `json:"server"`
	Port               int    `json:"port"`
	ConnectIP          string `json:"connectIp,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	Authentication     string `json:"authentication"`
	Username           string `json:"username,omitempty"`
	Password           string `json:"password,omitempty"`
}
type Settings struct {
	GlobalProxy string    `json:"globalProxy"`
	Profiles    []Profile `json:"profiles"`
}

// Policy is a private execution snapshot. Never publish it over settings/SSE.
type Policy struct {
	Profile *Profile `json:"profile,omitempty"`
}

func Defaults() Settings { return Settings{Profiles: []Profile{}} }
func Decode(data []byte, present bool) (Settings, error) {
	s := Defaults()
	if !present {
		return s, nil
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, errors.New("invalid network configuration")
	}
	if d.Decode(new(any)) != io.EOF {
		return s, errors.New("expected one network configuration object")
	}
	return s, s.Validate()
}
func Load(dir string) (Settings, error) {
	data, present, err := configstore.Read(dir, configstore.Network)
	if err != nil {
		return Settings{}, err
	}
	return Decode(data, present)
}
func validHost(s string) bool {
	if s == "" || len(s) > 253 || strings.ContainsAny(s, "/@?#\\ \t\n\r") {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	if strings.ContainsAny(s, ":[]") {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}
func validIP(s string) bool {
	if s == "" {
		return true
	}
	ip, e := netip.ParseAddr(s)
	return e == nil && ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
}
func (p Profile) Validate() error {
	if p.ID == "" || len(p.ID) > 80 {
		return errors.New("invalid proxy profile identity")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return errors.New("proxy name is required and must be at most 128 bytes")
	}
	if p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks5" {
		return errors.New("unsupported proxy protocol")
	}
	if !validHost(p.Server) {
		return errors.New("proxy server must be a hostname or IP, without a URL, path or credentials")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("proxy port must be from 1 to 65535")
	}
	if !validIP(p.ConnectIP) {
		return errors.New("invalid proxy connection IP")
	}
	if p.InsecureSkipVerify && p.Protocol != "https" {
		return errors.New("certificate verification can only be disabled for HTTPS proxies")
	}
	if p.Authentication != "none" && p.Authentication != "password" {
		return errors.New("unsupported proxy authentication")
	}
	if p.Authentication == "password" && (p.Username == "" || p.Password == "") {
		return errors.New("proxy username and password are required")
	}
	if len(p.Username) > 255 || len(p.Password) > 4096 || p.Protocol == "socks5" && len(p.Password) > 255 {
		return errors.New("proxy credentials exceed the protocol limit")
	}

	return nil
}
func (s Settings) Validate() error {
	if len(s.Profiles) > 64 {
		return errors.New("at most 64 proxy profiles are supported")
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, p := range s.Profiles {
		if err := p.Validate(); err != nil {
			return err
		}
		if ids[p.ID] || names[strings.ToLower(strings.TrimSpace(p.Name))] {
			return errors.New("proxy names and identities must be unique")
		}
		ids[p.ID] = true
		names[strings.ToLower(strings.TrimSpace(p.Name))] = true
	}
	if s.GlobalProxy != "" && !ids[s.GlobalProxy] {
		return errors.New("global proxy profile does not exist")
	}
	return nil
}
func (s Settings) Select(id string) (Policy, error) {
	p := Policy{}
	if id == "" {
		return p, nil
	}
	for _, v := range s.Profiles {
		if v.ID == id {
			p.Profile = &v
			return p, nil
		}
	}
	return p, errors.New("selected proxy profile does not exist")
}
func (p Policy) Validate() error {
	s := Settings{}
	if p.Profile != nil {
		s.Profiles = []Profile{*p.Profile}
	}
	return s.Validate()
}
func (p Profile) URL() *url.URL {
	u := &url.URL{Scheme: p.Protocol, Host: net.JoinHostPort(p.Server, fmt.Sprint(p.Port))}
	if p.Authentication == "password" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u
}
func (p Policy) Effective(u *url.URL) Policy {
	if p.Profile == nil {
		return p
	}
	host := u.Hostname()
	if loopbackHost(host) || net.ParseIP(host).IsLoopback() {
		p.Profile = nil
		return p
	}

	return p
}

type policyKey struct{}
type directoryKey struct{}

func WithConfigDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, directoryKey{}, dir)
}
func configDirectory(ctx context.Context) string {
	if dir, ok := ctx.Value(directoryKey{}).(string); ok {
		return dir
	}
	defaults.RLock()
	defer defaults.RUnlock()
	return defaults.dir
}
func WithPolicy(ctx context.Context, p Policy) context.Context {
	return context.WithValue(context.WithValue(ctx, scopeKey{}, ApplicationScope), policyKey{}, clonePolicy(p))
}
func FromContext(ctx context.Context) (Policy, error) {
	if p, ok := ctx.Value(policyKey{}).(Policy); ok {
		return clonePolicy(p), p.Validate()
	}
	if settings, ok, err := contextSettings(ctx); ok {
		if err != nil {
			return Policy{}, err
		}
		return settings.Select(settings.GlobalProxy)
	}
	dir := configDirectory(ctx)
	if dir == "" {
		return Policy{}, nil
	}
	s, e := Load(dir)
	if e != nil {
		return Policy{}, e
	}
	return s.Select(s.GlobalProxy)
}

var defaults struct {
	sync.RWMutex
	dir string
}

// SetConfigDir configures application-owned clients, not child environments.
func SetConfigDir(dir string) { defaults.Lock(); defaults.dir = dir; defaults.Unlock() }
func ForConnection(ctx context.Context, id string) (context.Context, error) {
	if current, ok := ctx.Value(policyKey{}).(Policy); ok && connectionScoped(ctx) {
		selected := ""
		if current.Profile != nil {
			selected = current.Profile.ID
		}
		if selected == id {
			return ctx, current.Validate()
		}
	}
	if settings, ok, err := contextSettings(ctx); ok {
		if err != nil {
			return ctx, err
		}
		policy, err := settings.Select(id)
		if err != nil {
			return ctx, err
		}
		return WithConnectionPolicy(ctx, policy), nil
	}
	dir := configDirectory(ctx)
	if dir == "" {
		if id != "" {
			return ctx, errors.New("network configuration is unavailable")
		}
		return WithConnectionPolicy(ctx, Policy{}), nil
	}
	s, e := Load(dir)
	if e != nil {
		return ctx, e
	}
	p, e := s.Select(id)
	if e != nil {
		return ctx, e
	}
	return WithConnectionPolicy(ctx, p), nil
}

type policyError struct {
	cause   error
	message string
}

func (e *policyError) Error() string { return e.message }
func (e *policyError) Unwrap() error { return e.cause }
func (e *policyError) Timeout() bool {
	var cause interface{ Timeout() bool }
	return errors.As(e.cause, &cause) && cause.Timeout()
}
func (e *policyError) Temporary() bool {
	var cause interface{ Temporary() bool }
	return errors.As(e.cause, &cause) && cause.Temporary()
}

// Sanitize prevents network errors from disclosing credentials or proxy URLs.
func Sanitize(err error, p Policy) error {
	if err == nil {
		return nil
	}
	if p.Profile == nil {
		return err
	}
	message := err.Error()
	for _, v := range []string{p.Profile.URL().String(), p.Profile.Password, p.Profile.Username} {
		if v != "" {
			message = strings.ReplaceAll(message, v, "[redacted]")
		}
	}
	return &policyError{cause: err, message: message}
}
