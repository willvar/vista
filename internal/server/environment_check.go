package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"vista/internal/network"
)

const (
	environmentCallback   = "/api/environment-check/callback/"
	environmentGlobalping = "https://api.globalping.io/v1/measurements"
	environmentCheckHost  = "https://check-host.net"
	environmentIPCow      = "https://ipcow.com/api/probe/http"
)

var environmentOrigin http.CrossOriginProtection

var environmentIPs = [2][]string{
	{"https://api.ipify.org/", "https://checkip.amazonaws.com/", "https://ipv4.icanhazip.com/"},
	{"https://api6.ipify.org/", "https://ipv6.icanhazip.com/", "https://v6.ident.me/"},
}

type environmentResult struct {
	Family string `json:"family"`
	Status string `json:"status"`
}

type environmentReport struct {
	Port    int                  `json:"port"`
	Results [2]environmentResult `json:"results"`
}

type environmentCheck struct {
	mu         sync.Mutex
	running    bool
	next       time.Time
	challenges map[string]*environmentChallenge
}

type environmentChallenge struct {
	proof  string
	served bool
	peers  []netip.Addr
}

func (a *App) environmentCheckHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := environmentOrigin.Check(r); err != nil {
		http.Error(w, "cross-origin request", http.StatusForbidden)
		return
	}
	if r.ContentLength != 0 || r.URL.ForceQuery || r.URL.RawQuery != "" {
		http.Error(w, "environment check takes no input", http.StatusBadRequest)
		return
	}
	port := 0
	if a.listenerPort != nil {
		port = a.listenerPort()
	}
	if port < 1 || port > 65535 {
		jsonOut(w, environmentReport{Results: [2]environmentResult{{"IPv4", "no_listener"}, {"IPv6", "no_listener"}}})
		return
	}
	check := &a.environmentCheck
	check.mu.Lock()
	busy := check.running || time.Now().Before(check.next)
	if !busy {
		check.running = true
		check.next = time.Now().Add(10 * time.Second)
	}
	check.mu.Unlock()
	if busy {
		http.Error(w, "environment check already active or recently started", http.StatusTooManyRequests)
		return
	}
	defer func() {
		check.mu.Lock()
		check.running = false
		check.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	client := newEnvironmentClient("tcp")
	defer client.CloseIdleConnections()
	report := environmentReport{Port: port}
	var wg sync.WaitGroup
	for index, family := range [2]string{"IPv4", "IPv6"} {
		wg.Go(func() {
			lookup := newEnvironmentClient([2]string{"tcp4", "tcp6"}[index])
			defer lookup.CloseIdleConnections()
			report.Results[index] = environmentResult{family, check.measure(ctx, lookup, client, index, port)}
		})
	}
	wg.Wait()
	jsonOut(w, report)
}

func (a *App) environmentCallbackHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("challenge")
	if r.URL.EscapedPath() != environmentCallback+id || r.URL.ForceQuery || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	check := &a.environmentCheck
	check.mu.Lock()
	challenge := check.challenges[id]
	var proof string
	if challenge != nil {
		proof = challenge.proof
		challenge.served = true
		// The direct socket is authoritative; never trust forwarding headers as a probe identity.
		if real, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && len(challenge.peers) < 16 {
			challenge.peers = append(challenge.peers, real.Addr().Unmap())
		}
	}
	check.mu.Unlock()
	if proof == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, proof)
}

// Use the OS route, not any Vista or environment HTTP proxy: the address of a
// configured proxy would describe that proxy, not this server's Internet route.
func newEnvironmentClient(family string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, family, address)
	}
	return network.NewClient(http.Client{Transport: network.Transport{Base: transport, Policy: &network.Policy{}}, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, network.ApplicationScope)
}

func environmentFetch(ctx context.Context, client *http.Client, endpoint string, input []byte, limit int64) ([]byte, error) {
	method := http.MethodGet
	if input != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain")
	req.Header.Set("User-Agent", "Vista (one-shot reachability diagnostic)")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("diagnostic provider HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if int64(len(body)) > limit {
		return nil, errors.New("diagnostic response exceeds limit")
	}
	return body, err
}

func environmentJSON(ctx context.Context, client *http.Client, url string, input any, out any) error {
	var payload []byte
	if input != nil {
		var err error
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	raw, err := environmentFetch(ctx, client, url, payload, 128<<10)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// Skip non-public and transition addresses even when a public lookup service
// returns them. IsGlobalUnicast alone also accepts private/special-use ranges.
var environmentIPv6 = netip.MustParsePrefix("2000::/3")
var environmentReserved = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func environmentPublicIP(ip netip.Addr) bool {
	if ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is4In6() || (ip.Is6() && !environmentIPv6.Contains(ip)) {
		return false
	}
	for _, prefix := range environmentReserved {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func (c *environmentCheck) measure(ctx context.Context, lookup, client *http.Client, family, port int) string {
	var target netip.Addr
	for _, endpoint := range environmentIPs[family] {
		raw, err := environmentFetch(ctx, lookup, endpoint, nil, 256)
		if err != nil {
			continue
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(string(raw)))
		if err == nil && ip.BitLen() == [2]int{32, 128}[family] && environmentPublicIP(ip) {
			target = ip
			break
		}
	}
	if !target.IsValid() {
		return "no_address"
	}
	backup := c.probeCheckHost
	if family == 1 {
		backup = c.probeIPCow
	}
	answered := false
	for _, probe := range []func(context.Context, *http.Client, netip.Addr, int, string) (bool, error){c.probeGlobalping, backup} {
		id := rand.Text()
		c.mu.Lock()
		if c.challenges == nil {
			c.challenges = make(map[string]*environmentChallenge)
		}
		c.challenges[id] = &environmentChallenge{proof: rand.Text()}
		c.mu.Unlock()
		attempt, cancel := context.WithTimeout(ctx, 12*time.Second)
		verified, err := probe(attempt, client, target, port, id)
		cancel()
		c.mu.Lock()
		delete(c.challenges, id)
		c.mu.Unlock()
		if verified {
			return "verified"
		}
		answered = answered || err == nil
		if ctx.Err() != nil {
			break
		}
	}
	if answered {
		return "no_callback"
	}
	return "probe_unavailable"
}

func (c *environmentCheck) witnessed(id, proof string, peer netip.Addr, ipv6 bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.challenges[id]
	if ch == nil || !ch.served {
		return false
	}
	if proof != "" {
		// A provider that returns the independently generated body proves the instance,
		// even if the request reached this listener through an existing forwarder.
		return ch.proof == proof
	}
	for _, seen := range ch.peers {
		if seen.Is6() == ipv6 && environmentPublicIP(seen) && (!peer.IsValid() || peer == seen) {
			return true
		}
	}
	return false
}

func environmentTarget(ip netip.Addr, port int, id string) url.URL {
	return url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), strconv.Itoa(port)), Path: environmentCallback + id}
}

func environmentSameURL(raw string, want url.URL) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != want.Scheme || u.User != nil || u.EscapedPath() != want.Path || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	gotIP, err := netip.ParseAddr(u.Hostname())
	wantIP, _ := netip.ParseAddr(want.Hostname())
	return err == nil && gotIP == wantIP && (u.Port() == want.Port() || (u.Port() == "" && want.Port() == "80"))
}

func environmentJobID(id string) bool {
	return len(id) > 0 && len(id) <= 100 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) == -1
}

func environmentPoll(ctx context.Context, examine func() (bool, bool, error)) (bool, error) {
	timer := time.NewTicker(750 * time.Millisecond)
	defer timer.Stop()
	for {
		verified, finished, err := examine()
		if verified || finished || err != nil {
			return verified, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *environmentCheck) probeGlobalping(ctx context.Context, client *http.Client, ip netip.Addr, port int, id string) (bool, error) {
	var job struct{ ID string }
	input := map[string]any{"type": "http", "target": ip.String(), "limit": 2,
		"measurementOptions": map[string]any{"protocol": "HTTP", "port": port,
			"request": map[string]string{"method": "GET", "path": environmentCallback + id}}}
	if err := environmentJSON(ctx, client, environmentGlobalping, input, &job); err != nil {
		return false, err
	}
	if !environmentJobID(job.ID) {
		return false, errors.New("invalid Globalping measurement ID")
	}
	return environmentPoll(ctx, func() (bool, bool, error) {
		var reply struct {
			Status  string
			Target  netip.Addr
			Results []struct {
				Result struct {
					Status          string
					StatusCode      int
					ResolvedAddress netip.Addr
					RawBody         string
					Truncated       *bool
				}
			}
		}
		if err := environmentJSON(ctx, client, environmentGlobalping+"/"+job.ID, nil, &reply); err != nil {
			return false, false, err
		}
		if reply.Target != ip {
			return false, false, errors.New("Globalping target mismatch")
		}
		for _, row := range reply.Results {
			r := row.Result
			if r.Status == "finished" && r.StatusCode == 200 && r.ResolvedAddress == ip && r.Truncated != nil && !*r.Truncated && r.RawBody != "" && c.witnessed(id, r.RawBody, netip.Addr{}, ip.Is6()) {
				return true, true, nil
			}
		}
		return false, reply.Status != "in-progress", nil
	})
}

func (c *environmentCheck) probeCheckHost(ctx context.Context, client *http.Client, ip netip.Addr, port int, id string) (bool, error) {
	target := environmentTarget(ip, port, id)
	query := url.Values{"host": {target.String()}, "max_nodes": {"2"}}
	var job struct {
		OK    int
		ID    string `json:"request_id"`
		Nodes map[string][]any
	}
	if err := environmentJSON(ctx, client, environmentCheckHost+"/check-http?"+query.Encode(), nil, &job); err != nil {
		return false, err
	}
	if job.OK != 1 || !environmentJobID(job.ID) || len(job.Nodes) == 0 {
		return false, errors.New("Check-Host did not schedule a check")
	}
	return environmentPoll(ctx, func() (bool, bool, error) {
		var reply struct {
			Command, Host string
			Results       map[string][][]any
		}
		if err := environmentJSON(ctx, client, environmentCheckHost+"/check-result-extended/"+job.ID, nil, &reply); err != nil {
			return false, false, err
		}
		if reply.Command != "http" || !environmentSameURL(reply.Host, target) {
			return false, false, errors.New("Check-Host target mismatch")
		}
		finished := true
		for name, node := range job.Nodes {
			rows := reply.Results[name]
			finished = finished && rows != nil
			if len(node) < 4 {
				continue
			}
			advertised, _ := node[3].(string)
			peer, err := netip.ParseAddr(advertised)
			if err != nil || !c.witnessed(id, "", peer, false) {
				continue
			}
			for _, row := range rows {
				if len(row) >= 5 && row[0] == float64(1) && row[3] == "200" && row[4] == ip.String() {
					return true, true, nil
				}
			}
		}
		return false, finished, nil
	})
}

func (c *environmentCheck) probeIPCow(ctx context.Context, client *http.Client, ip netip.Addr, port int, id string) (bool, error) {
	target := environmentTarget(ip, port, id)
	query := url.Values{"url": {target.String()}}
	var reply struct {
		Results map[string]struct {
			OK     bool
			Stack  string
			Status int
			URL    string `json:"final_url"`
		}
	}
	if err := environmentJSON(ctx, client, environmentIPCow+"?"+query.Encode(), nil, &reply); err != nil {
		return false, err
	}
	r, present := reply.Results["ipv6"]
	if !present {
		return false, errors.New("IP Cow returned no IPv6 result")
	}
	return r.OK && r.Stack == "ipv6" && r.Status == 200 && environmentSameURL(r.URL, target) && c.witnessed(id, "", netip.Addr{}, true), nil
}
