package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutomaticTitlesDefaultAndPersistence(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `{"delegationDepth":2}`} {
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, settingsFilename), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := ReadSettings(dir); err != nil || !got.AutomaticTitles {
			t.Fatalf("titles must default on, including existing settings: %+v %v", got, err)
		}
	}
	for _, enabled := range []bool{false, true} {
		settings := Settings{DelegationDepth: 3, AutomaticTitles: enabled}
		if err := WriteSettings(dir, settings); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadSettings(dir); err != nil || got != settings {
			t.Fatalf("title preference lost: %+v %v", got, err)
		}
	}
	for _, value := range []string{`null`, `0`, `"true"`, `{}`} {
		if err := os.WriteFile(filepath.Join(dir, settingsFilename), []byte(`{"delegationDepth":1,"automaticTitles":`+value+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSettings(dir); err == nil {
			t.Fatalf("invalid title preference accepted: %s", value)
		}
	}
}
