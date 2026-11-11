package server

import (
	"os"
	"path/filepath"
	"testing"

	"vista/internal/configstore"
)

func TestRuntimePortConfigurationPrecedence(t *testing.T) {
	t.Setenv("VISTA_PORT", "")
	t.Setenv("VISTA_HOST", "")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	check := func(override, want int) {
		t.Helper()
		config, err := LoadRuntimeConfig(dir, override)
		if err != nil || config.Port != want {
			t.Fatalf("port override %d: got %+v, %v; want %d", override, config, err, want)
		}
		if want == DefaultPort && config.Address() != "0.0.0.0:5186" {
			t.Fatalf("default listener does not cover all IPv4 interfaces: %q", config.Address())
		}
	}
	check(0, DefaultPort)
	if err := configstore.Write(dir, configstore.Server, []byte(`{"port":6186}`)); err != nil {
		t.Fatal(err)
	}
	check(0, 6186)
	t.Setenv("VISTA_PORT", "7186")
	check(0, 7186)
	check(8186, 8186)
	t.Setenv("VISTA_PORT", "invalid")
	check(8186, 8186)
}

func TestRuntimePortRejectsEphemeralAndInvalidConfiguration(t *testing.T) {
	t.Setenv("VISTA_PORT", "")
	t.Setenv("VISTA_HOST", "")
	for _, input := range []string{
		`{"port":0}`, `{"port":-1}`, `{"port":65536}`, `{"port":"5186"}`,
		`{"port":5186,"unknown":true}`, `{"port":5186}{}`, `null`, `[]`, ``,
	} {
		t.Run(input, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, configstore.Filename), []byte(`{"version":2,"server":`+input+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRuntimeConfig(dir, 0); err == nil {
				t.Fatalf("accepted invalid configuration %s", input)
			}
		})
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "-1", "65536", "invalid"} {
		t.Setenv("VISTA_PORT", value)
		if _, err := LoadRuntimeConfig(dir, 0); err == nil {
			t.Fatalf("accepted invalid VISTA_PORT=%q", value)
		}
	}
	t.Setenv("VISTA_PORT", "")
	for _, override := range []int{-1, 65536} {
		if _, err := LoadRuntimeConfig(dir, override); err == nil {
			t.Fatalf("accepted invalid port override %d", override)
		}
	}
}
