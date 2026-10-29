package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func newSearchSettingsApp(t *testing.T) *App {
	t.Helper()
	for _, name := range []string{"EXA", "FIRECRAWL", "PARALLEL", "TAVILY", "DEEPSEEK"} {
		t.Setenv(name+"_API_KEY", "")
		t.Setenv("VISTA_WEBSEARCH_"+name+"_API_KEY", "")
		t.Setenv("VISTA_WEBSEARCH_"+name+"_ENDPOINT", "")
	}
	providers, err := searchProviders()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{configDir: t.TempDir(), searchProviders: providers}
	if err := os.Chmod(app.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	app.searchConfig, err = loadSearchSettings(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func searchProviderNamed(t *testing.T, providers []workerproto.SearchProvider, name string) workerproto.SearchProvider {
	t.Helper()
	for _, provider := range providers {
		if provider.Name == name {
			return provider
		}
	}
	t.Fatalf("missing search provider %s", name)
	return workerproto.SearchProvider{}
}

func searchSettingsRequest(app *App, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	app.handler().ServeHTTP(w, httptest.NewRequest(method, "http://127.0.0.1:5186/api/search-config", strings.NewReader(body)))
	return w
}

func TestSearchSettingsPageCanBeRefreshed(t *testing.T) {
	app := &App{}
	w := httptest.NewRecorder()
	app.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186/settings/search", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), `src="/app.js"`) {
		t.Fatalf("search settings page cannot be opened directly: %d %s", w.Code, w.Body.String())
	}
}

func TestSearchSettingsPersistenceAndPrecedence(t *testing.T) {
	app := newSearchSettingsApp(t)
	t.Setenv("EXA_API_KEY", "exa-environment-secret")
	t.Setenv("VISTA_WEBSEARCH_PARALLEL_API_KEY", "parallel-environment-secret")
	t.Setenv("VISTA_WEBSEARCH_EXA_ENDPOINT", "http://127.0.0.1:4321/search")
	var err error
	app.searchProviders, err = searchProviders()
	if err != nil {
		t.Fatal(err)
	}
	checkSources := func(want []searchProviderSetting) {
		t.Helper()
		w := searchSettingsRequest(app, http.MethodGet, "")
		var got struct {
			Providers []searchProviderSetting `json:"providers"`
		}
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got.Providers, want) {
			t.Fatalf("settings metadata: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "apiKey") {
			t.Fatal("search settings exposed credentials")
		}
	}
	initial := []searchProviderSetting{{"firecrawl", "none"}, {"parallel", "environment"}, {"tavily", "none"}, {"exa", "environment"}, {"deepseek", "none"}}
	checkSources(initial)
	before := app.currentSearchProviders()
	for _, provider := range app.searchProviders {
		body := `{"name":"` + provider.Name + `","apiKey":"  ` + provider.Name + `-saved-secret  "}`
		if w := searchSettingsRequest(app, http.MethodPut, body); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("save %s: %d %s", provider.Name, w.Code, w.Body.String())
		}
	}
	checkSources([]searchProviderSetting{{"firecrawl", "settings"}, {"parallel", "settings"}, {"tavily", "settings"}, {"exa", "settings"}, {"deepseek", "settings"}})
	path := filepath.Join(app.configDir, searchSettingsFilename)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("search keys file is not private: %v %v", info, err)
	}
	settings, err := loadSearchSettings(app.configDir)
	if err != nil || len(settings.APIKeys) != 5 {
		t.Fatalf("reload settings: %v", err)
	}
	restarted := &App{configDir: app.configDir, searchProviders: app.searchProviders, searchConfig: settings}
	for _, provider := range restarted.currentSearchProviders() {
		if provider.APIKey != provider.Name+"-saved-secret" {
			t.Fatalf("%s saved key was not used after restart", provider.Name)
		}
	}
	if searchProviderNamed(t, before, "exa").APIKey != "exa-environment-secret" || searchProviderNamed(t, before, "firecrawl").APIKey != "" || searchProviderNamed(t, app.currentSearchProviders(), "exa").Endpoint != searchProviderNamed(t, before, "exa").Endpoint {
		t.Fatal("save changed an active run's snapshot or endpoint")
	}
	if w := searchSettingsRequest(app, http.MethodPut, `{"name":"exa","apiKey":"replacement-secret"}`); w.Code != http.StatusNoContent || searchProviderNamed(t, app.currentSearchProviders(), "exa").APIKey != "replacement-secret" {
		t.Fatal("could not replace saved key")
	}
	for _, provider := range app.searchProviders {
		if w := searchSettingsRequest(app, http.MethodDelete, `{"name":"`+provider.Name+`"}`); w.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", w.Code, w.Body.String())
		}
	}
	checkSources(initial)
	if !reflect.DeepEqual(before, app.currentSearchProviders()) {
		t.Fatal("deletion did not restore environment and anonymous access")
	}
	settings, err = loadSearchSettings(app.configDir)
	if err != nil || len(settings.APIKeys) != 0 {
		t.Fatalf("deleted keys persisted: %v", err)
	}
}

func TestJinaReaderKeyPersistenceAndRedaction(t *testing.T) {
	app := newSearchSettingsApp(t)
	get := func() searchProviderSetting {
		w := searchSettingsRequest(app, http.MethodGet, "")
		var response struct {
			JinaReader searchProviderSetting `json:"jinaReader"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("read Jina setting: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "apiKey") {
			t.Fatal("Jina key was exposed by the settings API")
		}
		return response.JinaReader
	}
	if got := get(); got != (searchProviderSetting{Name: "jina", Source: "none"}) {
		t.Fatalf("initial Jina setting: %+v", got)
	}
	if w := searchSettingsRequest(app, http.MethodPut, `{"name":"jina","apiKey":"jina-secret"}`); w.Code != http.StatusNoContent {
		t.Fatalf("save Jina key: %d %s", w.Code, w.Body.String())
	}
	if got := get(); got != (searchProviderSetting{Name: "jina", Source: "settings"}) {
		t.Fatalf("saved Jina setting: %+v", got)
	}
	settings, err := loadSearchSettings(app.configDir)
	if err != nil || settings.JinaAPIKey != "jina-secret" {
		t.Fatalf("reload Jina key: %q, %v", settings.JinaAPIKey, err)
	}
	if got := app.currentWebfetchSettings().JinaAPIKey; got != "jina-secret" {
		t.Fatalf("runtime Jina key: %q", got)
	}
	if w := searchSettingsRequest(app, http.MethodDelete, `{"name":"jina"}`); w.Code != http.StatusNoContent {
		t.Fatalf("remove Jina key: %d %s", w.Code, w.Body.String())
	}
	if got := get(); got != (searchProviderSetting{Name: "jina", Source: "none"}) {
		t.Fatalf("removed Jina setting: %+v", got)
	}
}

func TestDeepSeekSearchAndFetchKeysAreIndependent(t *testing.T) {
	app := newSearchSettingsApp(t)
	before := app.currentWebfetchSettings()
	for _, item := range []struct{ name, key string }{{"deepseek", "search-secret"}, {"deepseek-fetch", "fetch-secret"}} {
		if w := searchSettingsRequest(app, http.MethodPut, `{"name":"`+item.name+`","apiKey":"`+item.key+`"}`); w.Code != http.StatusNoContent {
			t.Fatal(w.Body.String())
		}
	}
	check := func(search, fetch string) {
		t.Helper()
		settings, err := loadSearchSettings(app.configDir)
		if err != nil || settings.APIKeys["deepseek"] != search || settings.DeepSeekFetchAPIKey != fetch {
			t.Fatalf("independent saved keys: %v", err)
		}
		if searchProviderNamed(t, app.currentSearchProviders(), "deepseek").APIKey != search || app.currentWebfetchSettings().DeepSeekAPIKey != fetch {
			t.Fatal("independent runtime keys lost")
		}
		w := searchSettingsRequest(app, http.MethodGet, "")
		var got struct {
			Providers     []searchProviderSetting
			DeepSeekFetch searchProviderSetting
		}
		if json.Unmarshal(w.Body.Bytes(), &got) != nil || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "apiKey") {
			t.Fatal("settings exposed keys")
		}
		wantSource := func(key string) string {
			if key == "" {
				return "none"
			}
			return "settings"
		}
		if got.DeepSeekFetch.Source != wantSource(fetch) {
			t.Fatal("incorrect fetch configuration status")
		}
		for _, provider := range got.Providers {
			if provider.Name == "deepseek" && provider.Source != wantSource(search) {
				t.Fatal("incorrect search configuration status")
			}
		}
	}
	check("search-secret", "fetch-secret")
	if w := searchSettingsRequest(app, http.MethodDelete, `{"name":"deepseek"}`); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	check("", "fetch-secret")
	if w := searchSettingsRequest(app, http.MethodPut, `{"name":"deepseek","apiKey":"new-search-secret"}`); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	if w := searchSettingsRequest(app, http.MethodDelete, `{"name":"deepseek-fetch"}`); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	check("new-search-secret", "")
	if before.DeepSeekAPIKey != "" {
		t.Fatal("key edits changed an active run snapshot")
	}
}

func TestSearchPreviewBytesPersistsWithoutChangingOrdersOrKeys(t *testing.T) {
	app := newSearchSettingsApp(t)
	if app.currentSearchPreviewBytes() != 50<<10 {
		t.Fatal("incorrect default search byte budget")
	}
	orders := append([]string(nil), app.searchConfig.Order...)
	fetch := app.currentWebfetchSettings()
	for _, limit := range []int{32, 0, 2000} {
		body, _ := json.Marshal(map[string]int{"searchPreviewBytes": limit})
		if w := searchSettingsRequest(app, http.MethodPatch, string(body)); w.Code != http.StatusNoContent {
			t.Fatal(w.Body.String())
		}
		settings, err := loadSearchSettings(app.configDir)
		if err != nil || settings.SearchPreviewBytes != limit || app.currentSearchPreviewBytes() != limit || !reflect.DeepEqual(settings.Order, orders) || !reflect.DeepEqual(app.currentWebfetchSettings(), fetch) {
			t.Fatalf("summary update changed unrelated settings: %v", err)
		}
	}
	// Even worst-case JSON escaping of all seven credentials fits the saved file.
	for _, name := range []string{"exa", "firecrawl", "parallel", "tavily", "deepseek", "jina", "deepseek-fetch"} {
		body, _ := json.Marshal(map[string]string{"name": name, "apiKey": strings.Repeat("<", maxSearchAPIKeyBytes)})
		if w := searchSettingsRequest(app, http.MethodPut, string(body)); w.Code != http.StatusNoContent {
			t.Fatal(w.Body.String())
		}
	}
	if _, err := loadSearchSettings(app.configDir); err != nil {
		t.Fatalf("accepted credentials cannot be reloaded: %v", err)
	}
}

func TestFetchPreviewBytesPersistsAndKeepsRunSnapshotsIndependent(t *testing.T) {
	app := newSearchSettingsApp(t)
	before := app.currentWebfetchSettings()
	if before.PreviewBytes != 50<<10 {
		t.Fatal("incorrect default page byte budget")
	}
	for _, limit := range []int{3, 0, 12000} {
		body, _ := json.Marshal(map[string]int{"fetchPreviewBytes": limit})
		if w := searchSettingsRequest(app, http.MethodPatch, string(body)); w.Code != http.StatusNoContent {
			t.Fatal(w.Body.String())
		}
		settings, err := loadSearchSettings(app.configDir)
		current := app.currentWebfetchSettings()
		if err != nil || settings.FetchPreviewBytes != limit || current.PreviewBytes != limit || !reflect.DeepEqual(current.Order, before.Order) || current.JinaAPIKey != before.JinaAPIKey || current.DeepSeekAPIKey != before.DeepSeekAPIKey || app.currentSearchPreviewBytes() != 50<<10 || before.PreviewBytes != 50<<10 {
			t.Fatalf("initial page length changed unrelated settings or an active snapshot: %+v %v", current, err)
		}
		var view struct {
			Length int `json:"fetchPreviewBytes"`
		}
		w := searchSettingsRequest(app, http.MethodGet, "")
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Length != limit {
			t.Fatalf("initial page length missing from settings: %s", w.Body.String())
		}
	}
}

func TestSearchSettingsRejectInvalidRequestsAndFailedWrites(t *testing.T) {
	app := newSearchSettingsApp(t)
	if w := searchSettingsRequest(app, http.MethodPut, `{"name":"exa","apiKey":"original-secret"}`); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	path := filepath.Join(app.configDir, searchSettingsFilename)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"name":"other","apiKey":"secret"}`, `{"name":"exa"}`, `{"name":"exa","apiKey":null}`,
		`{"name":"exa","apiKey":""}`, `{"name":"exa","apiKey":"   "}`, `{"name":"exa","apiKey":42}`,
		`{"name":"exa","apiKey":"a\nb"}`, `{"name":"exa","apiKey":"a\u0000b"}`, `{"name":"exa","apiKey":"secret","extra":true}`,
		`{"name":"exa","apiKey":"secret"} {}`, `{"name":"exa","apiKey":"` + strings.Repeat("x", 4097) + `"}`,
		`{"name":"exa","apiKey":"` + strings.Repeat("中", 1366) + `"}`, strings.Repeat(" ", 33<<10),
	} {
		if w := searchSettingsRequest(app, http.MethodPut, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"order":null}`, `{"order":[]}`,
		`{"searchPreviewBytes":-1}`, `{"searchPreviewBytes":1.5}`, `{"searchPreviewBytes":"1000"}`, `{"searchPreviewBytes":null}`,
		`{"fetchPreviewBytes":-1}`, `{"fetchPreviewBytes":1.5}`, `{"fetchPreviewBytes":"8000"}`, `{"fetchPreviewBytes":null}`,
		`{"fetchOrder":null}`, `{"fetchOrder":[]}`, `{"fetchOrder":["vista"]}`, `{"fetchOrder":["jina","jina"]}`,
		`{"fetchOrder":["vista","other"]}`, `{"fetchOrder":["vista",42]}`,
		`{"order":["exa","firecrawl","parallel","tavily","deepseek"],"fetchOrder":["vista","vista"]}`,
		`{"order":["native","firecrawl","parallel","tavily"]}`,
		`{"order":["native","firecrawl","parallel","tavily","exa","exa"]}`,
		`{"order":["native","firecrawl","parallel","tavily","tavily"]}`,
		`{"order":["other","firecrawl","parallel","tavily","exa"]}`,
		`{"order":["native","Firecrawl","parallel","tavily","exa"]}`,
		`{"order":["native","firecrawl","parallel","tavily",42]}`,
		`{"order":["native","firecrawl","parallel","tavily","exa"],"apiKeys":{}}`,
		`{"order":["native","firecrawl","parallel","tavily","exa"]} {}`,
	} {
		if w := searchSettingsRequest(app, http.MethodPatch, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid order request accepted: %d %s", w.Code, body)
		}
	}
	if w := searchSettingsRequest(app, http.MethodDelete, `{"name":"exa","apiKey":"secret"}`); w.Code != http.StatusBadRequest {
		t.Fatal("delete accepted a key")
	}
	if w := searchSettingsRequest(app, http.MethodPost, "{}"); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("unsupported method accepted")
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		request := httptest.NewRequest(method, "http://127.0.0.1:5186/api/search-config", strings.NewReader(`{}`))
		request.Header.Set("Origin", "https://example.com")
		w := httptest.NewRecorder()
		app.handler().ServeHTTP(w, request)
		if w.Code != http.StatusForbidden {
			t.Fatal("cross-origin write accepted")
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(original) {
		t.Fatal("invalid request modified saved credentials")
	}
	// Force atomic replacement to fail, even when tests run as root.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	before := app.currentSearchProviders()
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		body := `{"name":"exa"}`
		if method == http.MethodPut {
			body = `{"name":"exa","apiKey":"replacement-secret"}`
		} else if method == http.MethodPatch {
			body = `{"order":["tavily","parallel","exa","firecrawl","deepseek"]}`
		}
		if w := searchSettingsRequest(app, method, body); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("failed write: %d %s", w.Code, w.Body.String())
		}
		if !reflect.DeepEqual(before, app.currentSearchProviders()) {
			t.Fatal("failed write changed runtime credentials or order")
		}
	}
}

func TestSearchSettingsConcurrentUpdates(t *testing.T) {
	app := newSearchSettingsApp(t)
	var group sync.WaitGroup
	order := []string{"exa", "parallel", "tavily", "firecrawl", "deepseek"}
	group.Go(func() {
		body, _ := json.Marshal(map[string]any{"order": order})
		if w := searchSettingsRequest(app, http.MethodPatch, string(body)); w.Code != http.StatusNoContent {
			t.Errorf("concurrent reorder: %d %s", w.Code, w.Body.String())
		}
	})
	group.Go(func() {
		if w := searchSettingsRequest(app, http.MethodPatch, `{"fetchOrder":["deepseek","jina"]}`); w.Code != http.StatusNoContent {
			t.Errorf("concurrent webfetch reorder: %d %s", w.Code, w.Body.String())
		}
	})
	for _, name := range []string{"exa", "firecrawl", "parallel", "tavily", "deepseek"} {
		group.Go(func() {
			key := strings.Repeat("<", maxSearchAPIKeyBytes)
			body, _ := json.Marshal(map[string]string{"name": name, "apiKey": key})
			if w := searchSettingsRequest(app, http.MethodPut, string(body)); w.Code != http.StatusNoContent {
				t.Errorf("concurrent save: %d %s", w.Code, w.Body.String())
			}
			for range 10 {
				app.currentSearchProviders()
				searchSettingsRequest(app, http.MethodGet, "")
			}
		})
	}
	group.Wait()
	settings, err := loadSearchSettings(app.configDir)
	if err != nil || len(settings.APIKeys) != 5 {
		t.Fatalf("concurrent save lost a key: count=%d error=%v", len(settings.APIKeys), err)
	}
	if !reflect.DeepEqual(settings.Order, order) || !reflect.DeepEqual(app.searchConfig.Order, order) {
		t.Fatal("concurrent key update lost search order")
	}
	if !reflect.DeepEqual(settings.FetchOrder, []string{"deepseek", "jina"}) {
		t.Fatal("concurrent update lost webfetch order")
	}
	for _, provider := range app.currentSearchProviders() {
		if provider.APIKey != settings.APIKeys[provider.Name] {
			t.Fatal("runtime and persisted credentials differ")
		}
	}
}

func TestWebfetchOrderPersistsIndependentlyOfSearchAndKeys(t *testing.T) {
	app := newSearchSettingsApp(t)
	search := append([]string(nil), app.searchConfig.Order...)
	before := app.currentWebfetchSettings()
	get := func(want []string) {
		t.Helper()
		w := searchSettingsRequest(app, http.MethodGet, "")
		var result struct{ FetchOrder []string }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || !reflect.DeepEqual(result.FetchOrder, want) {
			t.Fatalf("webfetch order: %d %s", w.Code, w.Body.String())
		}
	}
	get([]string{"jina", "deepseek"})
	if w := searchSettingsRequest(app, http.MethodPatch, `{"fetchOrder":["deepseek","jina"]}`); w.Code != http.StatusNoContent {
		t.Fatalf("reorder webfetch: %d %s", w.Code, w.Body.String())
	}
	get([]string{"deepseek", "jina"})
	if !reflect.DeepEqual(app.searchConfig.Order, search) || !reflect.DeepEqual(before.Order, []string{"jina", "deepseek"}) {
		t.Fatal("webfetch update changed search order or an active run snapshot")
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		body := `{"name":"jina"}`
		if method == http.MethodPut {
			body = `{"name":"jina","apiKey":"jina-secret"}`
		}
		if w := searchSettingsRequest(app, method, body); w.Code != http.StatusNoContent {
			t.Fatal(w.Body.String())
		}
		saved, err := loadSearchSettings(app.configDir)
		if err != nil || !reflect.DeepEqual(saved.FetchOrder, []string{"deepseek", "jina"}) || !reflect.DeepEqual(saved.Order, search) {
			t.Fatalf("key update lost reading priority on restart: %+v %v", saved, err)
		}
	}
	snapshot := app.currentWebfetchSettings()
	snapshot.Order[0] = "invalid"
	get([]string{"deepseek", "jina"})
	// A failed atomic write must leave both the saved and active order intact.
	path := filepath.Join(app.configDir, searchSettingsFilename)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if w := searchSettingsRequest(app, http.MethodPatch, `{"fetchOrder":["jina","deepseek"]}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("failed write accepted: %d", w.Code)
	}
	get([]string{"deepseek", "jina"})
}

func TestSearchSettingsReachNextWorkerRun(t *testing.T) {
	app := newSearchSettingsApp(t)
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(ctx, "session", "search settings"); err != nil {
		t.Fatal(err)
	}
	sink := &imageCommandSink{}
	app.store, app.workers = db, map[string]*worker{"session": {input: sink}}
	order := []string{"tavily", "exa", "firecrawl", "parallel", "deepseek"}
	body, _ := json.Marshal(map[string]any{"order": order, "fetchOrder": []string{"deepseek", "jina"}, "searchPreviewBytes": 123, "fetchPreviewBytes": 345})
	if w := searchSettingsRequest(app, http.MethodPatch, string(body)); w.Code != http.StatusNoContent {
		t.Fatal("could not save search order")
	}
	for _, name := range []string{"exa", "firecrawl", "parallel", "tavily", "deepseek"} {
		if w := searchSettingsRequest(app, http.MethodPut, `{"name":"`+name+`","apiKey":"`+name+`-secret"}`); w.Code != http.StatusNoContent {
			t.Fatal("could not save search key")
		}
	}
	if w := searchSettingsRequest(app, http.MethodPut, `{"name":"jina","apiKey":"jina-secret"}`); w.Code != http.StatusNoContent {
		t.Fatal("could not save Jina key")
	}
	if w := searchSettingsRequest(app, http.MethodPut, `{"name":"deepseek-fetch","apiKey":"fetch-only-secret"}`); w.Code != http.StatusNoContent {
		t.Fatal("could not save DeepSeek fetch key")
	}
	message, _, err := db.BeginRun(ctx, "session", "run", "message", "search the web")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.dispatchRun(ctx, "session", "run", message, modelcfg.Model{ID: "model"}); err != nil {
		t.Fatal(err)
	}
	var command workerproto.Command
	if err := json.NewDecoder(sink).Decode(&command); err != nil || len(command.SearchProviders) != 5 {
		t.Fatalf("worker search configuration missing: %v", err)
	}
	for i, provider := range command.SearchProviders {
		if provider.Name != order[i] {
			t.Fatal("worker did not receive the saved search order")
		}
		if provider.APIKey != provider.Name+"-secret" {
			t.Fatalf("worker did not receive saved %s key", provider.Name)
		}
	}
	if !reflect.DeepEqual(command.Webfetch.Order, []string{"deepseek", "jina"}) {
		t.Fatalf("worker webfetch order: %v", command.Webfetch.Order)
	}
	if command.Webfetch.JinaAPIKey != "jina-secret" {
		t.Fatalf("worker did not receive Jina Reader key: %q", command.Webfetch.JinaAPIKey)
	}
	if command.SearchPreviewBytes != 123 || command.Webfetch.PreviewBytes != 345 || command.Webfetch.DeepSeekAPIKey != "fetch-only-secret" {
		t.Fatal("worker did not receive initial content lengths or independent fetch key")
	}
}

func TestSearchSettingsInitialOrderIsPersistedOnce(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new config", true: "existing keys"}[legacy], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, searchSettingsFilename)
			if legacy {
				if err := os.WriteFile(path, []byte(`{"apiKeys":{"exa":"legacy-secret"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			first, err := loadSearchSettings(dir)
			if err != nil || !validSearchOrder(first.Order) {
				t.Fatalf("initial order must include every external provider: %v %v", first.Order, err)
			}
			if !reflect.DeepEqual(first.FetchOrder, []string{"jina", "deepseek"}) {
				t.Fatalf("initial webfetch order must be Jina first: %v", first.FetchOrder)
			}
			if legacy && first.APIKeys["exa"] != "legacy-secret" {
				t.Fatal("initializing order lost existing credentials")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("initial settings were not persisted privately")
			}
			for range 10 {
				reloaded, err := loadSearchSettings(dir)
				if err != nil || !reflect.DeepEqual(reloaded, first) {
					t.Fatalf("restart reshuffled initial order: %v", err)
				}
				if current, err := os.Stat(path); err != nil || !current.ModTime().Equal(info.ModTime()) {
					t.Fatal("restart rewrote initialized settings")
				}
			}
		})
	}
}

func TestSearchSettingsOrderSurvivesKeyUpdatesAndRestart(t *testing.T) {
	app := newSearchSettingsApp(t)
	before := app.currentSearchProviders()
	order := []string{"tavily", "parallel", "exa", "firecrawl", "deepseek"}
	body, _ := json.Marshal(map[string]any{"order": order})
	if w := searchSettingsRequest(app, http.MethodPatch, string(body)); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		body := `{"name":"exa"}`
		if method == http.MethodPut {
			body = `{"name":"exa","apiKey":"secret"}`
		}
		if w := searchSettingsRequest(app, method, body); w.Code != http.StatusNoContent {
			t.Fatal(w.Body.String())
		}
		reloaded, err := loadSearchSettings(app.configDir)
		if err != nil || !reflect.DeepEqual(reloaded.Order, order) {
			t.Fatalf("saved order changed after key update/restart: %v", err)
		}
		restarted := &App{searchConfig: reloaded, searchProviders: app.searchProviders}
		for range 3 {
			w := searchSettingsRequest(restarted, http.MethodGet, "")
			var got struct{ Order []string }
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got.Order, order) {
				t.Fatalf("settings refresh changed saved order: %s", w.Body.String())
			}
		}
		for i, provider := range restarted.currentSearchProviders() {
			if provider.Name != order[i] {
				t.Fatal("run snapshot does not follow saved priority")
			}
		}
	}
	if len(before) != 5 {
		t.Fatal("unexpected initial run snapshot")
	}
}

func TestSearchSettingsInitializationFailureAndInvalidSavedOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSearchSettings(dir); err == nil {
		t.Fatal("unpersisted random order accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{`[]`, `["native","exa","exa","tavily","parallel"]`, `["other","exa","firecrawl","tavily","parallel"]`} {
		data := `{"apiKeys":{"exa":"secret"},"order":` + order + `}`
		path := filepath.Join(dir, searchSettingsFilename)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSearchSettings(dir); err == nil {
			t.Fatal("invalid saved order accepted")
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != data {
			t.Fatal("invalid settings were overwritten")
		}
	}
}

func TestSearchSettingsRemoveBuiltInReaderPreservingExternalOrderAndKeys(t *testing.T) {
	for _, order := range []string{`["vista","deepseek","jina"]`, `["deepseek","vista","jina"]`, `["deepseek","jina","vista"]`} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		original := `{"order":["exa","firecrawl","parallel","tavily","deepseek"],"fetchOrder":` + order + `,"apiKeys":{"exa":"search-key"},"jinaApiKey":"reader-key","deepseekFetchApiKey":"fetch-key","searchPreviewBytes":123,"fetchPreviewBytes":456}`
		if err := os.WriteFile(filepath.Join(dir, searchSettingsFilename), []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		config, err := loadSearchSettings(dir)
		if err != nil || !reflect.DeepEqual(config.FetchOrder, []string{"deepseek", "jina"}) || config.APIKeys["exa"] != "search-key" || config.JinaAPIKey != "reader-key" || config.DeepSeekFetchAPIKey != "fetch-key" || config.SearchPreviewBytes != 123 || config.FetchPreviewBytes != 456 {
			t.Fatalf("normalization lost user settings: %v", err)
		}
		reloaded, err := loadSearchSettings(dir)
		if err != nil || !reflect.DeepEqual(config, reloaded) {
			t.Fatalf("normalization was not durable: %v", err)
		}
	}
}

func TestSearchSettingsMigrateCharacterLimitsToBytesOnce(t *testing.T) {
	for _, tc := range []struct {
		name, limits  string
		search, fetch int
	}{
		{"defaults", `"summaryLength":1000,"fetchPreviewLength":8000`, 50 << 10, 50 << 10},
		{"unlimited", `"summaryLength":0,"fetchPreviewLength":0`, 0, 0},
		{"custom capacity", `"summaryLength":2345,"fetchPreviewLength":12000`, 74 << 10, 47 << 10},
		{"new settings win", `"summaryLength":1000,"fetchPreviewLength":8000,"searchPreviewBytes":12345,"fetchPreviewBytes":0`, 12345, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, searchSettingsFilename)
			original := `{"apiKeys":{"exa":"search-key"},"jinaApiKey":"reader-key","order":["exa","firecrawl","parallel","tavily","deepseek"],"fetchOrder":["deepseek","jina"],` + tc.limits + `}`
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			settings, err := loadSearchSettings(dir)
			if err != nil || settings.SearchPreviewBytes != tc.search || settings.FetchPreviewBytes != tc.fetch || settings.APIKeys["exa"] != "search-key" || settings.JinaAPIKey != "reader-key" || !reflect.DeepEqual(settings.FetchOrder, []string{"deepseek", "jina"}) {
				t.Fatalf("migration lost limits, order or keys: search=%d fetch=%d err=%v", settings.SearchPreviewBytes, settings.FetchPreviewBytes, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || strings.Contains(string(data), `"summaryLength"`) || strings.Contains(string(data), `"fetchPreviewLength"`) {
				t.Fatal("legacy units remain in saved settings")
			}
			reloaded, err := loadSearchSettings(dir)
			if err != nil || !reflect.DeepEqual(settings, reloaded) {
				t.Fatalf("byte settings were converted twice: %v", err)
			}
		})
	}
}
