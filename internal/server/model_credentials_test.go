package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestWorkerCredentialBrokerRejectsChangedConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer long-lived-pat" {
			t.Error("incorrect exchange credential")
		}
		fmt.Fprint(w, `{"token":"short-token","headers":{}}`)
	}))
	defer upstream.Close()
	app := &App{models: modelCatalog{Default: "saved", Models: []configuredModel{{Key: "saved", Provider: "openai-chat", ProviderID: "gitlab", ID: "test", BaseURL: upstream.URL, InstanceURL: upstream.URL, APIKey: "long-lived-pat"}}}}
	selected, _, err := app.resolveForRun(context.Background(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey != "short-token" || selected.GitLabInstanceURL != "" || selected.CredentialRef == "" {
		t.Fatal("worker received source material or no renewal reference")
	}
	acked := make(chan workerproto.Command, 1)
	consumer := workerResultConsumer{ctx: context.Background(), refreshCredentials: app.refreshModelCredentials, send: func(ack workerproto.Command) error { acked <- ack; return nil }}
	result := workerproto.Result{Kind: "model_credentials", AckID: "request", Credential: &workerproto.CredentialRequest{Key: selected.Key, Ref: selected.CredentialRef}}
	if err := consumer.consume(result); err != nil {
		t.Fatal(err)
	}
	select {
	case ack := <-acked:
		if ack.Error != "" || ack.Model.APIKey != "short-token" {
			t.Fatalf("refresh failed: %s", ack.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not acknowledge")
	}
	app.models.Models[0].BaseURL = "https://changed.example"
	if _, err := app.refreshModelCredentials(context.Background(), selected); err == nil {
		t.Fatal("old worker refreshed against changed endpoint")
	}
	app.models.Models = nil
	if _, err := app.refreshModelCredentials(context.Background(), selected); err == nil {
		t.Fatal("disconnected account still renewed")
	}
}

func TestCredentialExchangeDoesNotBlockWorkerResultReader(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	acked := make(chan workerproto.Command, 1)
	consumer := workerResultConsumer{ctx: context.Background(), refreshCredentials: func(ctx context.Context, _ modelcfg.Model) (modelcfg.Model, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return modelcfg.Model{}, ctx.Err()
	}, send: func(ack workerproto.Command) error { acked <- ack; return nil }}
	request := workerproto.Result{Kind: "model_credentials", AckID: "credential-ack", Credential: &workerproto.CredentialRequest{Key: "cloud", Ref: strings.Repeat("a", 64), Deadline: time.Now().Add(50 * time.Millisecond).UnixMilli()}}
	if err := consumer.consume(request); err != nil {
		t.Fatal(err)
	}
	<-started
	select {
	case ack := <-acked:
		if ack.Error == "" {
			t.Fatal("expired request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("credential exchange ignored deadline")
	}
}
