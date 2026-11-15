package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/codexauth"
	"vista/internal/copilotauth"
	modelcfg "vista/internal/model"
	"vista/internal/modelcatalog"
	"vista/internal/network"
	"vista/internal/provider"
)

func TestAuthorizationAndDiscoveryUseSelectedProxy(t *testing.T) {
	cases := []struct {
		name string
		call func(context.Context, network.Policy) error
	}{
		{"codex-login", func(ctx context.Context, p network.Policy) error {
			ctx, err := network.ForConnection(ctx, p.Profile.ID)
			if err != nil {
				return err
			}
			client := codexauth.NewClient()
			client.Issuer = "https://codex.fixture.invalid"
			_, err = client.Begin(ctx)
			return err
		}},
		{"copilot-login", func(ctx context.Context, p network.Policy) error {
			ctx, err := network.ForConnection(ctx, p.Profile.ID)
			if err != nil {
				return err
			}
			client := copilotauth.NewClient()
			client.Issuer = "https://copilot.fixture.invalid"
			_, err = client.Begin(ctx)
			return err
		}},
		{"custom-models", func(ctx context.Context, p network.Policy) error {
			_, err := modelcatalog.DiscoverModels(ctx, modelcatalog.Configured{ProviderID: "custom", Provider: "openai-chat", BaseURL: "https://custom.fixture.invalid", Authentication: "none", ProxyID: p.Profile.ID})
			return err
		}},
		{"zen-models", func(ctx context.Context, p network.Policy) error {
			_, err := modelcatalog.DiscoverModels(ctx, modelcatalog.Configured{ProviderID: "opencode", APIKey: "synthetic", ProxyID: p.Profile.ID})
			return err
		}},
		{"local-models", func(ctx context.Context, p network.Policy) error {
			_, err := modelcatalog.DiscoverModels(ctx, modelcatalog.Configured{ProviderID: "vllm", BaseURL: "https://vllm.fixture.invalid", ProxyID: p.Profile.ID})
			return err
		}},
		{"copilot-models", func(ctx context.Context, p network.Policy) error {
			_, err := modelcatalog.DiscoverModels(ctx, modelcatalog.Configured{ProviderID: "github-copilot", ProxyID: p.Profile.ID, BaseURL: "https://copilot.fixture.invalid", APIKey: "synthetic"})
			return err
		}},
		{"gitlab-exchange", func(ctx context.Context, p network.Policy) error {
			_, _, err := provider.ResolveRunCredentials(ctx, modelcfg.Model{ProviderID: "gitlab", APIKey: "synthetic", GitLabInstanceURL: "https://gitlab.fixture.invalid", ProxyID: p.Profile.ID}, "fixture")
			return err
		}},
		{"ibm-exchange", func(ctx context.Context, p network.Policy) error {
			_, _, err := provider.ResolveRunCredentials(ctx, modelcfg.Model{ProviderID: "watsonx", APIKey: "synthetic", ProjectID: "fixture", ProxyID: p.Profile.ID}, "fixture")
			return err
		}},
		{"sap-exchange", func(ctx context.Context, p network.Policy) error {
			key, _ := json.Marshal(map[string]string{"url": "https://sap-auth.fixture.invalid", "clientid": "fixture", "clientsecret": "synthetic"})
			_, _, err := provider.ResolveRunCredentials(ctx, modelcfg.Model{ProviderID: "sap-ai-core", APIKey: string(key), BaseURL: "https://sap-api.fixture.invalid", DeploymentID: "fixture", ProxyID: p.Profile.ID}, "fixture")
			return err
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			var proxyCalls atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				http.Error(w, "isolated proxy observation completed", http.StatusBadGateway)
			}))
			defer proxy.Close()
			u, _ := url.Parse(proxy.URL)
			port, _ := strconv.Atoi(u.Port())
			p := network.Policy{Profile: &network.Profile{ID: "fixture-proxy", Name: "Fixture proxy", Protocol: "http", Server: u.Hostname(), Port: port, Authentication: "none"}}
			var mu sync.Mutex
			var direct []string
			transport := &http.Transport{DialContext: func(ctx context.Context, kind, address string) (net.Conn, error) {
				if address == proxy.Listener.Addr().String() {
					return (&net.Dialer{}).DialContext(ctx, kind, address)
				}
				mu.Lock()
				direct = append(direct, address)
				mu.Unlock()
				return nil, errors.New("direct egress blocked by isolated test fixture")
			}}
			old := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { (network.Transport{Base: transport}).CloseIdleConnections(); http.DefaultTransport = old }()
			global := *p.Profile
			global.ID, global.Server = "global-proxy", "global-proxy.invalid"
			settings := network.Settings{GlobalProxy: global.ID, Profiles: []network.Profile{global, *p.Profile}}
			globalPolicy, _ := settings.Select(global.ID)
			ctx, cancel := context.WithTimeout(network.WithPolicy(network.WithSettings(context.Background(), settings), globalPolicy), 3*time.Second)
			defer cancel()
			err := item.call(ctx, p)
			actualProxyCalls := proxyCalls.Load()
			mu.Lock()
			observed := append([]string(nil), direct...)
			mu.Unlock()
			// A control through the explicit network layer proves that this same
			// native transport/context can reach the selected proxy without DNS
			// or any external connection. The proxy deliberately returns 502.
			request, _ := http.NewRequestWithContext(network.WithConnectionPolicy(ctx, p), http.MethodGet, "https://control.fixture.invalid/", nil)
			response, _ := network.Do(&http.Client{Timeout: time.Second}, request)
			if response != nil {
				response.Body.Close()
			}
			if proxyCalls.Load() <= actualProxyCalls {
				t.Fatal("fixture control did not reach the selected proxy")
			}
			if len(observed) > 0 || actualProxyCalls == 0 {
				t.Errorf("selected proxy was bypassed: direct_dial_targets=%v proxy_requests=%d error=%v", observed, actualProxyCalls, err)
			}
		})
	}
}
