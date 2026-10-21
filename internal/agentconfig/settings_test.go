package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDelegationSettingsRoundTripAndBounds(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSettings(dir); err != nil || got.DelegationDepth != DefaultDelegationDepth {
		t.Fatalf("default delegation settings: %+v, %v", got, err)
	}
	if err := Write(dir, "Keep these global instructions."); err != nil {
		t.Fatal(err)
	}
	for _, depth := range []int{0, 1, 2, MaxDelegationDepth} {
		if err := WriteSettings(dir, Settings{DelegationDepth: depth}); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadSettings(dir); err != nil || got.DelegationDepth != depth {
			t.Fatalf("saved delegation depth %d: %+v, %v", depth, got, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, settingsFilename)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings file permissions: %v, %v", info, err)
	}
	for _, depth := range []int{-1, MaxDelegationDepth + 1} {
		if err := WriteSettings(dir, Settings{DelegationDepth: depth}); err == nil {
			t.Fatalf("invalid depth %d accepted", depth)
		}
	}
	if got, err := ReadSettings(dir); err != nil || got.DelegationDepth != MaxDelegationDepth {
		t.Fatalf("invalid write changed saved settings: %+v, %v", got, err)
	}
	if got, err := Read(dir); err != nil || got != "Keep these global instructions." {
		t.Fatalf("settings changed instructions: %q, %v", got, err)
	}
	if err := Write(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSettings(dir); err != nil || got.DelegationDepth != MaxDelegationDepth {
		t.Fatalf("clearing instructions changed delegation settings: %+v, %v", got, err)
	}
}

func TestDelegationSettingsRejectCorruptFile(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"delegationDepth":null}`, `{"delegationDepth":9}`, `{"delegationDepth":"2"}`, `{"delegationDepth":1.5}`, `{"delegationDepth":2,"other":true}`, `{"delegationDepth":2}{}`} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, settingsFilename), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if settings, err := ReadSettings(dir); err == nil {
				t.Fatalf("corrupt file accepted: %+v", settings)
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsFilename), []byte(`{"delegationDepth":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if settings, err := ReadSettings(dir); err != nil || settings.DelegationDepth != 0 {
		t.Fatalf("explicit zero rejected: %+v, %v", settings, err)
	}
}

func TestDelegationSettingsRequirePrivateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := WriteSettings(dir, Settings{DelegationDepth: 2}); err == nil {
		t.Fatal("non-private configuration directory accepted")
	}
}
