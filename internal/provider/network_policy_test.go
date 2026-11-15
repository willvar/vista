package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

func TestModelConnectionPolicyRoutesAndSnapshots(t *testing.T) {
	var direct, global, selected, edited, failed atomic.Int32
	handler := func(counter *atomic.Int32, failure bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			if r.Host != "model.fixture.invalid" || r.Header.Get("Authorization") != "Bearer fixture" {
				t.Error("route changed target identity or credentials")
			}
			io.Copy(io.Discard, r.Body)
			if failure {
				http.Error(w, "fixture proxy failure", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}
	upstream := httptest.NewServer(handler(&direct, false))
	defer upstream.Close()
	var profiles []network.Profile
	for i, counter := range []*atomic.Int32{&global, &selected, &edited, &failed} {
		server := httptest.NewServer(handler(counter, i == 3))
		defer server.Close()
		u, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(u.Port())
		profiles = append(profiles, network.Profile{ID: fmt.Sprint(i), Name: fmt.Sprint(i), Protocol: "http", Server: u.Hostname(), Port: port, Authentication: "none"})
	}
	previousDial := modelHTTPTransport.DialContext
	modelHTTPTransport.DialContext = func(ctx context.Context, kind, address string) (net.Conn, error) {
		if address == "model.fixture.invalid:80" {
			address = upstream.Listener.Addr().String()
		}
		return new(net.Dialer).DialContext(ctx, kind, address)
	}
	t.Cleanup(func() {
		(network.Transport{Base: modelHTTPTransport}).CloseIdleConnections()
		modelHTTPTransport.DialContext = previousDial
	})
	settings := network.Settings{GlobalProxy: profiles[0].ID, Profiles: profiles}
	globalPolicy, _ := settings.Select(settings.GlobalProxy)
	ctx := network.WithPolicy(network.WithSettings(context.Background(), settings), globalPolicy)
	source := modelcfg.Model{Provider: "openai-chat", ProviderID: "custom", Custom: true, Authentication: "bearer", ID: "fixture", BaseURL: "http://model.fixture.invalid/v1", APIKey: "fixture", ProxyID: profiles[1].ID}
	call := func(ctx context.Context, model modelcfg.Model, wantError bool) {
		t.Helper()
		turn, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
		if (err != nil) != wantError || !wantError && turn.Text != "ok" {
			t.Fatalf("turn=%+v err=%v", turn, err)
		}
	}
	call(ctx, source, false)
	if selected.Load() != 1 || global.Load() != 0 || direct.Load() != 0 {
		t.Fatal("connection did not override global proxy")
	}
	_, snapshot, err := source.BindNetwork(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Profiles = append([]network.Profile(nil), profiles...)
	settings.Profiles[1].Port = profiles[2].Port
	newCtx := network.WithPolicy(network.WithSettings(context.Background(), settings), globalPolicy)
	call(newCtx, snapshot, false)
	call(newCtx, source, false)
	if selected.Load() != 2 || edited.Load() != 1 {
		t.Fatal("active snapshot or new-operation route changed incorrectly")
	}
	source.ProxyID = ""
	call(ctx, source, false)
	if direct.Load() != 1 || global.Load() != 0 {
		t.Fatal("explicit Direct inherited global proxy")
	}
	source.ProxyID = "missing"
	call(ctx, source, true)
	source.ProxyID = profiles[3].ID
	call(ctx, source, true)
	if failed.Load() != 1 || direct.Load() != 1 || global.Load() != 0 {
		t.Fatal("failed/missing proxy silently changed route")
	}
}

func TestCredentialCacheUsesCurrentOperationPolicy(t *testing.T) {
	previous := catalogAdapters["watsonx"]
	t.Cleanup(func() { catalogAdapters["watsonx"] = previous })
	exchanges := 0
	catalogAdapters["watsonx"] = catalogAdapter{prepareRun: func(ctx context.Context, model modelcfg.Model, key string) (modelcfg.Model, string, error) {
		exchanges++
		if _, err := network.ConnectionPolicy(ctx); err != nil {
			t.Fatal(err)
		}
		model.APIKey, model.CredentialExpiresAt = "exchanged-token", time.Now().Add(time.Hour)
		return model, key, nil
	}}
	profile := network.Profile{ID: "selected", Name: "Selected", Protocol: "http", Server: "proxy.invalid", Port: 8080, Authentication: "none"}
	ctx := network.WithSettings(context.Background(), network.Settings{GlobalProxy: profile.ID, Profiles: []network.Profile{profile}})
	source := modelcfg.Model{ProviderID: "watsonx", Provider: "openai-chat", APIKey: "saved-key", ProxyID: profile.ID}
	var cache CredentialCache
	first, _, err := cache.Resolve(ctx, source, "model")
	if err != nil || first.Network == nil || first.Network.Profile == nil {
		t.Fatalf("initial resolve: %v", err)
	}
	source.ProxyID = ""
	second, _, err := cache.Resolve(ctx, source, "model")
	if err != nil || second.Network == nil || second.Network.Profile != nil || second.ProxyID != "" || exchanges != 1 {
		t.Fatalf("cached credentials restored old route: %+v err=%v exchanges=%d", second.Network, err, exchanges)
	}
	source.ProxyID = "missing"
	if _, _, err := cache.Resolve(ctx, source, "model"); err == nil || exchanges != 1 {
		t.Fatal("cache hit bypassed connection validation")
	}
}
