package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutputPagePathCannotEscapeStateDirectory(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "outputs")
	if err := os.Mkdir(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outputDir, "link.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	app := &App{dir: root}
	if _, err := app.openOutput(secret); err == nil {
		t.Fatal("path outside outputs was opened")
	}
	if file, err := app.openOutput(link); err == nil {
		file.Close()
		t.Fatal("symlink outside outputs was followed")
	}
	if err := app.removeOutput(link); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "secret" {
		t.Fatalf("deleting an output symlink touched outside file: %q %v", data, err)
	}
}
