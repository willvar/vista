package network

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"vista/internal/configstore"
)

func writeOperationSettings(t *testing.T, dir, server string) {
	t.Helper()
	settings := Settings{GlobalProxy: "a"}
	for _, id := range []string{"a", "b"} {
		settings.Profiles = append(settings.Profiles, Profile{ID: id, Name: id, Protocol: "http", Server: server, Port: 8080, Authentication: "none"})
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := configstore.Write(dir, configstore.Network, data); err != nil {
		t.Fatal(err)
	}
}

func TestOperationSettingsCaptureOnFirstSelectionAndRemainStable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeOperationSettings(t, dir, "before.example")
	parent := WithConfigDir(context.Background(), dir)
	ctx := WithOperation(parent)
	writeOperationSettings(t, dir, "first-selection.example")
	selected, err := ForConnection(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ConnectionPolicy(selected)
	if err != nil || policy.Profile == nil || policy.Profile.Server != "first-selection.example" {
		t.Fatalf("connection policy was captured too early: %+v %v", policy, err)
	}
	writeOperationSettings(t, dir, "next-operation.example")
	var readers sync.WaitGroup
	for range 16 {
		readers.Go(func() {
			global, err := FromContext(WithOperation(ctx))
			if err != nil || global.Profile == nil || global.Profile.ID != "a" || global.Profile.Server != "first-selection.example" {
				t.Errorf("global selection reread the operation's configuration: %+v %v", global, err)
			}
			connection, err := ForConnection(ctx, "b")
			if err != nil {
				t.Error(err)
				return
			}
			policy, err := ConnectionPolicy(connection)
			if err != nil || policy.Profile == nil || policy.Profile.ID != "b" || policy.Profile.Server != "first-selection.example" {
				t.Errorf("connection selection changed within the operation: %+v %v", policy, err)
			}
		})
	}
	readers.Wait()
	current, err := FromContext(WithOperation(parent))
	if err != nil || current.Profile == nil || current.Profile.Server != "next-operation.example" {
		t.Fatalf("next operation did not read current settings: %+v %v", current, err)
	}
}

func TestOperationSettingsRetainErrorsAndRespectExplicitPolicies(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configstore.Filename), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := WithConfigDir(context.Background(), dir)
	ctx := WithOperation(parent)
	_, first := FromContext(ctx)
	if first == nil {
		t.Fatal("invalid configuration was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, configstore.Filename), []byte(`{"version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ForConnection(ctx, ""); err != first {
		t.Fatalf("operation lost its original configuration error: %v, want %v", err, first)
	}
	if policy, err := FromContext(WithOperation(parent)); err != nil || policy.Profile != nil {
		t.Fatalf("next operation did not recover: %+v %v", policy, err)
	}
	explicit := WithOperation(WithConnectionPolicy(ctx, Policy{}))
	if _, err := ConnectionPolicy(explicit); err != nil {
		t.Fatalf("explicit Direct snapshot tried to load settings: %v", err)
	}
	if _, err := ForConnection(explicit, ""); err != nil {
		t.Fatalf("explicit connection snapshot was replaced: %v", err)
	}
	if policy, err := FromContext(WithPolicy(ctx, Policy{})); err != nil || policy.Profile != nil {
		t.Fatalf("explicit global policy was replaced: %+v %v", policy, err)
	}
	if _, err := FromContext(WithOperation(WithSettings(ctx, Defaults()))); err != nil {
		t.Fatalf("explicit settings snapshot was replaced: %v", err)
	}
}
