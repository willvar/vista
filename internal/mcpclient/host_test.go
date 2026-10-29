package mcpclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestHostedOAuthSnapshotsPersistRotationAndPreserveNewerAuthorization(t *testing.T) {
	var refreshes atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" {
			http.Error(w, "invalid refresh", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		n := refreshes.Add(1)
		fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":3600}`, n, n)
	}))
	defer tokens.Close()
	config := Config{ID: "oauth", Name: "OAuth", Transport: "http", Endpoint: tokens.URL + "/mcp", OAuth: true}
	dir, snapshotDir := t.TempDir(), t.TempDir()
	original := oauthCredential{Endpoint: config.Endpoint, Config: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{TokenURL: tokens.URL}}, Token: oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Hour)}}
	if err := writeOAuth(oauthPath(dir, config.ID), original); err != nil {
		t.Fatal(err)
	}
	version, snapshot := OAuthSnapshot(dir, config)
	if snapshot == nil || snapshot(snapshotDir) != nil {
		t.Fatal("could not create authorization snapshot")
	}
	source := &storedTokenSource{path: oauthPath(snapshotDir, config.ID), endpoint: config.Endpoint}
	if token, err := source.Token(); err != nil || token.RefreshToken != "refresh-1" {
		t.Fatalf("snapshot refresh: %+v %v", token, err)
	}
	stored, err := readOAuth(oauthPath(dir, config.ID), config.Endpoint)
	if err != nil || stored.Token.RefreshToken != "refresh-1" || stored.OriginPath != "" || stored.Generation != version {
		t.Fatalf("rotated token was not persisted: %+v %v", stored, err)
	}
	if next, _ := OAuthSnapshot(dir, config); next != version {
		t.Fatal("token refresh changed the connection version")
	}
	// A newly completed login is a new grant, even with an identical endpoint.
	staging := t.TempDir()
	original.Token.AccessToken, original.Token.RefreshToken = "new-login", "new-login-refresh"
	if err := writeOAuth(oauthPath(staging, config.ID), original); err != nil {
		t.Fatal(err)
	}
	if err := CommitOAuth(staging, dir, config); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []bool{false, true} {
		if removed {
			if err := RemoveOAuth(dir, config.ID); err != nil {
				t.Fatal(err)
			}
		}
		value, err := readOAuth(source.path, config.Endpoint)
		if err != nil {
			t.Fatal(err)
		}
		value.Token.Expiry = time.Now().Add(-time.Hour)
		if err := writeOAuth(source.path, value); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Token(); err != nil {
			t.Fatalf("old execution lost its authorization: %v", err)
		}
		stored, err := readOAuth(oauthPath(dir, config.ID), config.Endpoint)
		if removed {
			if !os.IsNotExist(err) {
				t.Fatal("old snapshot recreated deleted authorization")
			}
		} else if err != nil || stored.Token.AccessToken != "new-login" {
			t.Fatal("old snapshot overwrote a newer login")
		}
	}
}
