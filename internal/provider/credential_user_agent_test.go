package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/useragent"
)

func TestCredentialExchangesKeepNativeHTTPIdentity(t *testing.T) {
	dir := configureTestUserAgent(t)
	if err := os.WriteFile(filepath.Join(dir, useragent.Filename), []byte(`{"preset":`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gitlab", "sap", "watsonx", "vertex"} {
		t.Run(name, func(t *testing.T) {
			agents := make(chan string, 1)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				agents <- r.UserAgent()
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"capture complete"}`)
			}))
			defer endpoint.Close()
			var err error
			switch name {
			case "gitlab":
				_, _, err = resolveGitLabForRun(context.Background(), modelcfg.Model{APIKey: "test-pat", GitLabInstanceURL: endpoint.URL}, "fixture")
			case "sap":
				key, _ := json.Marshal(map[string]string{"url": endpoint.URL, "clientid": "test-client", "clientsecret": "test-secret"})
				_, _, err = resolveSAPAIForRun(context.Background(), modelcfg.Model{APIKey: string(key), BaseURL: endpoint.URL}, "fixture")
			case "watsonx":
				original := watsonxIAMEndpoint
				watsonxIAMEndpoint = endpoint.URL
				t.Cleanup(func() { watsonxIAMEndpoint = original })
				_, _, err = resolveWatsonxForRun(context.Background(), modelcfg.Model{APIKey: "test-key", ProjectID: "test-project"}, "fixture")
			case "vertex":
				key, keyErr := rsa.GenerateKey(rand.Reader, 2048)
				if keyErr != nil {
					t.Fatal(keyErr)
				}
				credentials, _ := json.Marshal(map[string]string{
					"type": "service_account", "project_id": "test-project", "client_email": "test@example.com",
					"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
					"token_uri":   endpoint.URL,
				})
				path := filepath.Join(t.TempDir(), "credentials.json")
				if err := os.WriteFile(path, credentials, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
				_, _, err = resolveVertexForRun(context.Background(), modelcfg.Model{}, "fixture")
			}
			if err == nil {
				t.Fatal("expected capture endpoint's HTTP 400")
			}
			select {
			case got := <-agents:
				if got == "" || got == "ConfiguredClient/1.0" {
					t.Errorf("credential exchange identity replaced: %q", got)
				}
			default:
				t.Fatalf("credential request failed before dispatch: %v", err)
			}
		})
	}
}
