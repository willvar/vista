package appdirs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigUsesPlatformDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Config()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "vista"); got != want {
		t.Fatalf("Config() = %q, want %q", got, want)
	}
}

func TestConfigAndStateUseSeparateDirectories(t *testing.T) {
	config, err := Config()
	if err != nil {
		t.Fatal(err)
	}
	state, err := State()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(config, "state"); state != want {
		t.Fatalf("State() = %q, want %q", state, want)
	}
}

func TestConfigHonorsXDGOverride(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME override is verified on Linux")
	}
	root := filepath.Join(string(filepath.Separator), "vista-test-config")
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(string(filepath.Separator), "unused-state"))
	config, err := Config()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "vista"); config != want {
		t.Fatalf("Config() = %q, want %q", config, want)
	}
	state, err := State()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "vista", "state"); state != want {
		t.Fatalf("State() = %q, want %q", state, want)
	}
}

func TestConfigRejectsRelativeDirectories(t *testing.T) {
	for _, root := range []string{"", ".", "relative/config"} {
		if got, err := configDir(root); err == nil || got != "" {
			t.Fatalf("configDir(%q) = %q, %v; want rejection", root, got, err)
		}
	}
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_CONFIG_HOME", "relative/config")
		if got, err := Config(); err == nil || got != "" {
			t.Fatalf("relative XDG_CONFIG_HOME: Config() = %q, %v", got, err)
		}
		if got, err := State(); err == nil || got != "" {
			t.Fatalf("relative XDG_CONFIG_HOME: State() = %q, %v", got, err)
		}
	}
}
