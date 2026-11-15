package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"vista/internal/configstore"
	"vista/internal/network"
)

func networkTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return &App{configDir: dir, dir: filepath.Join(dir, "state")}
}
func networkTestRequest(t *testing.T, a *App, method string, input any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(input)
	r := httptest.NewRequest(method, "/api/network-config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	a.networkConfigHTTP(w, r)
	return w
}
func networkTestView(t *testing.T, a *App) networkConfigView {
	t.Helper()
	w := networkTestRequest(t, a, http.MethodGet, nil)
	if w.Code != 200 {
		t.Fatalf("network GET status=%d body=%s", w.Code, w.Body.String())
	}
	var view networkConfigView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatal("invalid network view")
	}
	return view
}
func networkTestProfile() network.Profile {
	return network.Profile{ID: "profile", Name: "Example proxy", Protocol: "http", Server: "proxy.example", Port: 8080, Authentication: "password", Username: "temporary-user", Password: "temporary-profile-secret"}
}

func TestNetworkSnapshotUsesCapturedCheckpointVersion(t *testing.T) {
	app := networkTestApp(t)
	previous := app.updates.snapshot()
	app.updates.notify("network")
	point := app.updates.snapshot()

	// A later commit finishes after the stream captures its checkpoint, but
	// before it reads the configuration. It must remain a later stream update.
	settings := network.Settings{Profiles: []network.Profile{networkTestProfile()}, GlobalProxy: "profile"}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := configstore.Write(app.configDir, configstore.Network, data); err != nil {
		t.Fatal(err)
	}
	app.updates.notify("network")
	if err := app.addStateSnapshots(t.Context(), "localhost", &point, previous); err != nil {
		t.Fatal(err)
	}
	expected := point.Epoch + ":" + strconv.FormatUint(point.Revisions["network"], 10)
	if point.Network == nil || point.Network.Version != expected {
		t.Fatalf("network snapshot acknowledged a later commit: want version=%s, snapshot=%+v", expected, point.Network)
	}
	if point.Network.Config.GlobalProxy != settings.GlobalProxy {
		t.Fatal("snapshot did not read the saved configuration")
	}
	fresh := networkTestView(t, app)
	if fresh.Version == expected || fresh.Revision != point.Network.Revision {
		t.Fatal("later commit lost its newer version or content revision")
	}
}

func TestNetworkAPIProtectsCredentialsAndRevisions(t *testing.T) {
	app := networkTestApp(t)
	initial := networkTestView(t, app)
	if initial.Config.GlobalProxy != "" || len(initial.Config.Profiles) != 0 || initial.Revision == "" {
		t.Fatal("network defaults are not direct")
	}
	profile := networkTestProfile()
	candidate := network.Settings{Profiles: []network.Profile{profile}}
	saved := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: initial.Revision})
	if saved.Code != 200 {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	if strings.Contains(saved.Body.String(), profile.Username) || strings.Contains(saved.Body.String(), profile.Password) {
		t.Fatal("credentials returned over settings")
	}
	view := networkTestView(t, app)
	if len(view.Config.Profiles) != 1 || !view.Config.Profiles[0].HasCredentials {
		t.Fatal("credential presence not reported")
	}
	candidate.Profiles[0].Username = ""
	candidate.Profiles[0].Password = ""
	candidate.Profiles[0].Server = "changed.example"
	blocked := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: view.Revision})
	if blocked.Code != 400 {
		t.Fatalf("credentials reused for new entry without consent: %d", blocked.Code)
	}
	consent := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: view.Revision, ReuseCredentials: []string{profile.ID}})
	if consent.Code != 200 {
		t.Fatalf("explicit reuse failed: %d %s", consent.Code, consent.Body.String())
	}
	stale := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: view.Revision})
	if stale.Code != 409 {
		t.Fatal("stale revision overwrote network settings")
	}
	latest := networkTestView(t, app)
	candidate.Profiles[0].Authentication = "none"
	cleared := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: latest.Revision})
	if cleared.Code != 200 {
		t.Fatal(cleared.Code)
	}
	private, err := os.ReadFile(filepath.Join(app.configDir, configstore.KeyFilename))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(private, []byte(profile.Password)) {
		t.Fatal("cleared credential still referenced in key file")
	}
}
func TestNetworkDeletionListsSavedReferences(t *testing.T) {
	app := networkTestApp(t)
	profile := networkTestProfile()
	initial := networkTestView(t, app)
	candidate := network.Settings{GlobalProxy: profile.ID, Profiles: []network.Profile{profile}}
	if w := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: initial.Revision}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	models := []byte(`{"default":"model","models":[{"key":"model","connectionId":"connection","name":"Example connection","provider":"openai-chat","id":"example-model","apiKey":"temporary-model-key","proxyId":"profile"}]}`)
	if err := configstore.Write(app.configDir, configstore.Models, models); err != nil {
		t.Fatal(err)
	}
	view := networkTestView(t, app)
	candidate.Profiles = nil
	w := networkTestRequest(t, app, http.MethodPut, networkConfigInput{Config: candidate, Revision: view.Revision})
	if w.Code != 409 {
		t.Fatalf("referenced profile deletion status=%d", w.Code)
	}
	var result struct {
		Code    string `json:"code"`
		Details struct {
			References []networkReference `json:"references"`
		} `json:"details"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Code != "proxy_in_use" || len(result.Details.References) != 2 {
		t.Fatalf("reference locations missing: %s", w.Body.String())
	}
	var global, provider bool
	for _, ref := range result.Details.References {
		if ref.Kind == "global" {
			global = true
		}
		if ref.Kind == "provider" && ref.Name == "Example connection" && ref.Key == "model" {
			provider = true
		}
	}
	if !global || !provider {
		t.Fatal("reference navigation targets missing")
	}
}

func TestNetworkProxyCertificateSettingRoundTrip(t *testing.T) {
	app := networkTestApp(t)
	profile := networkTestProfile()
	profile.Protocol = "https"
	profile.Port = 443
	for _, skip := range []bool{false, true, false} {
		previous := networkTestView(t, app)
		profile.InsecureSkipVerify = skip
		input := networkConfigInput{Config: network.Settings{Profiles: []network.Profile{profile}}, Revision: previous.Revision}
		response := networkTestRequest(t, app, http.MethodPut, input)
		if response.Code != http.StatusOK {
			t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
		}
		view := networkTestView(t, app)
		if len(view.Config.Profiles) != 1 || view.Config.Profiles[0].InsecureSkipVerify != skip {
			t.Fatal("public snapshot lost certificate setting")
		}
		loaded, err := network.Load(app.configDir)
		if err != nil || loaded.Profiles[0].InsecureSkipVerify != skip {
			t.Fatalf("saved certificate setting lost: %+v %v", loaded, err)
		}
	}
}

func TestNetworkOnlyProviderEditPreservesAdvancedFields(t *testing.T) {
	app := networkTestApp(t)
	profile := networkTestProfile()
	profile.Authentication = "none"
	profile.Username = ""
	profile.Password = ""
	data, _ := json.Marshal(network.Settings{Profiles: []network.Profile{profile}})
	if err := configstore.Write(app.configDir, configstore.Network, data); err != nil {
		t.Fatal(err)
	}
	model := []byte(`{"default":"model","models":[{"key":"model","connectionId":"advanced","name":"Advanced connection","provider":"openai-chat","providerId":"custom","authentication":"bearer","baseUrl":"https://api.example/v1","id":"gpt-6-luna","apiKey":"temporary-model-key","reasoningSummary":"detailed","headers":{"X-Example":"temporary-header"}}]}`)
	if err := configstore.Write(app.configDir, configstore.Models, model); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.modelSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"key": "model", "proxyId": profile.ID, "revision": app.modelSettingsVersion(snapshot.revision)})
	response := httptest.NewRecorder()
	app.providerSettings(response, httptest.NewRequest(http.MethodPatch, "/api/provider-config", bytes.NewReader(body)))
	if response.Code != 204 {
		t.Fatalf("binding status=%d body=%s", response.Code, response.Body.String())
	}
	stored, _, err := configstore.Read(app.configDir, configstore.Models)
	if err != nil {
		t.Fatal(err)
	}
	var models struct {
		Models []struct {
			ProxyID          string            `json:"proxyId"`
			APIKey           string            `json:"apiKey"`
			ReasoningSummary string            `json:"reasoningSummary"`
			Headers          map[string]string `json:"headers"`
		} `json:"models"`
	}
	json.Unmarshal(stored, &models)
	if len(models.Models) != 1 || models.Models[0].ProxyID != profile.ID || models.Models[0].APIKey != "temporary-model-key" || models.Models[0].ReasoningSummary != "detailed" || models.Models[0].Headers["X-Example"] != "temporary-header" {
		t.Fatal("network-only edit changed advanced or private fields")
	}
	stale := httptest.NewRecorder()
	app.providerSettings(stale, httptest.NewRequest(http.MethodPatch, "/api/provider-config", bytes.NewReader(body)))
	if stale.Code != 409 {
		t.Fatal("stale model binding was saved")
	}
}
