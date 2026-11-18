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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/netutil"

	"vista/internal/network"
)

const (
	environmentCallback   = "/api/environment-check/callback/"
	environmentGlobalping = "https://api.globalping.io/v1/measurements"
	environmentCheckHost  = "https://check-host.net"
	environmentIPCow      = "https://ipcow.com/api/probe/http"
)

var environmentOrigin http.CrossOriginProtection

var environmentPorts = [...]int{80, 443, 8080, 8443, 3306, 5432}
var environmentFamilies = [2]string{"tcp4", "tcp6"}

var environmentIPs = [2][]string{
	{"https://api.ipify.org/", "https://checkip.amazonaws.com/", "https://ipv4.icanhazip.com/"},
	{"https://api6.ipify.org/", "https://ipv6.icanhazip.com/", "https://v6.ident.me/"},
}

type environmentResult struct {
	Family         string `json:"family"`
	Status         string `json:"status"`
	Port           int    `json:"port,omitempty"`
	AttemptedPorts []int  `json:"attemptedPorts,omitempty"`
}

type environmentReport struct {
	Results [2]environmentResult `json:"results"`
}

type environmentUpdate struct {
	Step   string             `json:"step,omitempty"`
	Report *environmentReport `json:"report,omitempty"`
}

type environmentCheck struct {
	mu         sync.Mutex
	running    bool
	next       time.Time
	challenges map[string]*environmentChallenge
}

type environmentChallenge struct {
	family int
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

	ports := a.environmentCandidatePorts()
	// Allow address discovery and two providers per family, even on different ports.
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	client := newEnvironmentClient("tcp")
	defer client.CloseIdleConnections()
	lookup := [2]*http.Client{newEnvironmentClient("tcp4"), newEnvironmentClient("tcp6")}
	defer lookup[0].CloseIdleConnections()
	defer lookup[1].CloseIdleConnections()
	var config net.ListenConfig
	run := func(progress func(string)) environmentReport {
		return check.scan(ctx, lookup, client, ports, config.Listen, progress)
	}
	if r.Header.Get("Accept") != "text/event-stream" {
		jsonOut(w, run(nil))
		return
	}
	streamEnvironmentCheck(w, r.Context(), cancel, run)
}

// This progress belongs to one diagnostic request, not shared application state.
func streamEnvironmentCheck(w http.ResponseWriter, requestContext context.Context, cancel context.CancelFunc, run func(func(string)) environmentReport) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(update environmentUpdate) error {
		payload, err := json.Marshal(update)
		if err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		return controller.Flush()
	}

	connected := true
	report := run(func(step string) {
		if connected && write(environmentUpdate{Step: step}) != nil {
			connected = false
			cancel()
		}
	})
	if connected && requestContext.Err() == nil {
		_ = write(environmentUpdate{Report: &report})
	}
}

func (a *App) environmentCandidatePorts() []int {
	ports := environmentPorts[:]
	if a.listenerPort == nil {
		return ports
	}
	port := a.listenerPort()
	if port > 0 && port < 65535 && !slices.Contains(ports, port+1) {
		ports = append(ports, port+1)
	}
	return ports
}

func (c *environmentCheck) scan(ctx context.Context, lookup [2]*http.Client, client *http.Client, ports []int, listen func(context.Context, string, string) (net.Listener, error), progress func(string)) environmentReport {
	var report environmentReport
	var targets [2]netip.Addr
	if progress == nil {
		progress = func(string) {}
	}
	progress("address")
	var wg sync.WaitGroup
	for index, name := range [2]string{"IPv4", "IPv6"} {
		wg.Go(func() {
			targets[index] = environmentLookup(ctx, lookup[index], index)
			status := "no_address"
			if targets[index].IsValid() {
				status = "no_listener"
			}
			report.Results[index] = environmentResult{Family: name, Status: status}
		})
	}
	wg.Wait()
	if ctx.Err() != nil || (!targets[0].IsValid() && !targets[1].IsValid()) {
		return report
	}
	progress("port")
	var firstProbe sync.Once
	for _, port := range ports {
		if ctx.Err() != nil {
			break
		}
		for family, target := range targets {
			result := &report.Results[family]
			// Only a bind failure advances to another port. A failed callback does not.
			if !target.IsValid() || len(result.AttemptedPorts) != 0 {
				continue
			}
			wg.Go(func() {
				ln, err := listen(ctx, environmentFamilies[family], net.JoinHostPort("", strconv.Itoa(port)))
				if err != nil {
					return // Skip ports we cannot bind; never displace a service or elevate privileges.
				}
				if ctx.Err() == nil {
					firstProbe.Do(func() { progress("callback") })
				}
				if ctx.Err() != nil {
					_ = ln.Close()
					return
				}
				result.AttemptedPorts = append(result.AttemptedPorts, port)
				result.Status = c.servePort(ctx, client, ln, target, family, port)
				if result.Status == "verified" {
					result.Port = port
				}
			})
		}
		wg.Wait() // Finish both families and release this port before trying the next.
	}
	return report
}

func (c *environmentCheck) servePort(ctx context.Context, client *http.Client, ln net.Listener, target netip.Addr, family, port int) string {
	srv := &http.Server{ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, MaxHeaderBytes: 4 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.callbackHTTP(w, r, family) })}
	srv.SetKeepAlivesEnabled(false)
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(netutil.LimitListener(ln, 8)) }()
	defer func() { _ = srv.Close(); <-done }()
	return c.measurePort(ctx, client, target, family, port)
}

func (c *environmentCheck) callbackHTTP(w http.ResponseWriter, r *http.Request, family int) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := strings.CutPrefix(r.URL.EscapedPath(), environmentCallback)
	if !ok || !environmentJobID(id) || r.URL.ForceQuery || r.URL.RawQuery != "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	challenge := c.challenges[id]
	var proof string
	if challenge != nil && challenge.family == family {
		proof = challenge.proof
		challenge.served = true
		// Never use forwarding headers as a probe identity.
		if real, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && len(challenge.peers) < 16 {
			challenge.peers = append(challenge.peers, real.Addr().Unmap())
		}
	}
	c.mu.Unlock()
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

func environmentLookup(ctx context.Context, lookup *http.Client, family int) netip.Addr {
	for _, endpoint := range environmentIPs[family] {
		if ctx.Err() != nil {
			break
		}
		attempt, cancel := context.WithTimeout(ctx, 4*time.Second)
		raw, err := environmentFetch(attempt, lookup, endpoint, nil, 256)
		cancel()
		if err != nil {
			continue
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(string(raw)))
		if err == nil && ip.BitLen() == [2]int{32, 128}[family] && environmentPublicIP(ip) {
			return ip
		}
	}
	return netip.Addr{}
}

func (c *environmentCheck) measurePort(ctx context.Context, client *http.Client, target netip.Addr, family, port int) string {
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
		c.challenges[id] = &environmentChallenge{family: family, proof: rand.Text()}
		c.mu.Unlock()
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
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
