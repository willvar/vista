package modelcatalog

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestVertexCatalogRoutesAndCredentials(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.json")
	data := []byte(`{"default":"gemini","models":[{"key":"gemini","provider":"catalog","providerId":"google-vertex","id":"gemini-2.5-flash"},{"key":"claude","provider":"catalog","providerId":"google-vertex","id":"claude-haiku-4-5@20251001"},{"key":"maas","provider":"catalog","providerId":"google-vertex","id":"openai/gpt-oss-120b-maas"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_VERTEX_PROJECT", "test-project")
	t.Setenv("GOOGLE_VERTEX_LOCATION", "us-central1")
	t.Setenv("GOOGLE_VERTEX_ENDPOINT", "")
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	regional := "https://us-central1-aiplatform.googleapis.com"
	for _, check := range []struct{ key, protocol, base string }{
		{"gemini", "vertex-gemini", regional + "/v1beta1/projects/test-project/locations/us-central1/publishers/google"},
		{"claude", "vertex-anthropic", regional + "/v1/projects/test-project/locations/us-central1/publishers/anthropic"},
		{"maas", "openai-chat", "https://aiplatform.googleapis.com/v1/projects/test-project/locations/us-central1/endpoints/openapi"},
	} {
		model, _, err := catalog.Resolve(check.key)
		if err != nil || model.Provider != check.protocol || model.BaseURL != check.base || model.APIKey != "" {
			t.Errorf("Vertex %q: %+v, err=%v", check.key, model, err)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"gemini-2.5-flash"`, `"gemini-2.5-flash","project":"unsafe/path"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("unsafe Vertex project accepted")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	requestCount := 0
	tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != http.MethodPost || r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.FormValue("assertion") == "" {
			t.Errorf("Vertex token request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"adc-token-` + strconv.Itoa(requestCount) + `","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenEndpoint.Close()
	credentialFile := filepath.Join(root, "google.json")
	credentialData, err := json.Marshal(map[string]string{"type": "service_account", "project_id": "test-project", "client_email": "vista@test-project.iam.gserviceaccount.com", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})), "token_uri": tokenEndpoint.URL})
	if err != nil || os.WriteFile(credentialFile, credentialData, 0600) != nil {
		t.Fatal("write test Google credentials")
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentialFile)
	catalog, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.ResolveForRun(context.Background(), "gemini")
	if err != nil || model.APIKey != "adc-token-1" || requestCount != 1 {
		t.Fatalf("Vertex ADC resolution: token=%q, requests=%d, err=%v", model.APIKey, requestCount, err)
	}
	model, _, err = catalog.ResolveForRun(context.Background(), "gemini")
	if err != nil || model.APIKey != "adc-token-2" || requestCount != 2 {
		t.Fatalf("Vertex ADC refresh: token=%q, requests=%d, err=%v", model.APIKey, requestCount, err)
	}
	workerEnv := WorkerEnvironment([]string{"PATH=/usr/bin", "GOOGLE_APPLICATION_CREDENTIALS=" + credentialFile})
	if len(workerEnv) != 1 || workerEnv[0] != "PATH=/usr/bin" {
		t.Fatalf("Google credential path reached Worker: %q", workerEnv)
	}
	if err := os.WriteFile(path, []byte(`{"default":"gemini","models":[{"key":"gemini","provider":"catalog","providerId":"google-vertex","id":"gemini-2.5-flash"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	t.Setenv("GOOGLE_VERTEX_API_KEY", "express-key")
	catalog, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = catalog.ResolveForRun(context.Background(), "")
	if err != nil || model.APIKey != "express-key" || !model.VertexAPIKey || model.BaseURL != "https://aiplatform.googleapis.com/v1/publishers/google" || requestCount != 2 {
		t.Fatalf("Vertex Express Mode: %+v, token requests=%d, err=%v", model, requestCount, err)
	}
	if err := os.WriteFile(path, []byte(`{"default":"gemini","models":[{"key":"gemini","provider":"catalog","providerId":"google-vertex","id":"gemini-2.5-flash","accessToken":"short-token","project":"test-project"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = catalog.ResolveForRun(context.Background(), "")
	if err != nil || model.APIKey != "short-token" || model.VertexAPIKey || requestCount != 2 {
		t.Fatalf("Vertex explicit OAuth token: %+v, token requests=%d, err=%v", model, requestCount, err)
	}
}
