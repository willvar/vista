package configstore_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/configstore"
)

func privateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLegacyMigrationPreservesSourceAndSeparatesCredentials(t *testing.T) {
	dir := privateConfigDir(t)
	legacy := []byte("{\n  \"default\":\"example\", \"models\":[{\"key\":\"example\",\"provider\":\"openai\",\"id\":\"example\",\"apiKey\":\"private-api-key\",\"headers\":{\"X-Custom\":\"private-header\"}}]\n}\n")
	instructions := []byte("Keep these instructions unchanged.\n")
	for name, data := range map[string][]byte{"models.json": legacy, "AGENTS.md": instructions} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := configstore.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(dir, configstore.Filename))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := os.ReadFile(filepath.Join(dir, configstore.KeyFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-api-key", "private-header"} {
		if bytes.Contains(main, []byte(secret)) || !bytes.Contains(keys, []byte(secret)) {
			t.Fatalf("credential separation failed for %q", secret)
		}
	}
	resolved, present, err := configstore.Read(dir, configstore.Models)
	if err != nil || !present || !bytes.Contains(resolved, []byte("private-api-key")) || !bytes.Contains(resolved, []byte("private-header")) {
		t.Fatalf("resolved settings: present=%v err=%v", present, err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".config-backup-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups: %v %v", backups, err)
	}
	copy, err := os.ReadFile(filepath.Join(backups[0], "models.json"))
	if err != nil || !bytes.Equal(copy, legacy) {
		t.Fatalf("source backup changed: %v", err)
	}
	sums, err := os.ReadFile(filepath.Join(backups[0], "SHA256SUMS"))
	if err != nil || !strings.Contains(string(sums), fmt.Sprintf("%x  models.json", sha256.Sum256(legacy))) {
		t.Fatalf("backup checksum: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "models.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source was not retired: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil || !bytes.Equal(got, instructions) {
		t.Fatalf("instructions changed: %v", err)
	}
	if err := configstore.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := filepath.Glob(filepath.Join(dir, ".config-backup-*"))
	if len(after) != 1 {
		t.Fatalf("healthy startup repeated migration: %v", after)
	}
}

func TestNormalAccessDoesNotMigrateLegacySettings(t *testing.T) {
	dir := privateConfigDir(t)
	path := filepath.Join(dir, "server.json")
	legacy := []byte(`{"port":5186}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configstore.Read(dir, configstore.Server); !errors.Is(err, configstore.ErrNeedsMigration) {
		t.Fatalf("read should require startup migration: %v", err)
	}
	if err := configstore.Write(dir, configstore.Server, []byte(`{"port":5187}`)); !errors.Is(err, configstore.ErrNeedsMigration) {
		t.Fatalf("write should require startup migration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, configstore.Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("normal access created a main file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("legacy settings changed: %v", err)
	}
}

func TestSectionRevisionIgnoresUnrelatedUpdates(t *testing.T) {
	dir := privateConfigDir(t)
	if err := configstore.Write(dir, configstore.Server, []byte(`{"port":5186}`)); err != nil {
		t.Fatal(err)
	}
	before, present, err := configstore.Read(dir, configstore.Server)
	if err != nil {
		t.Fatal(err)
	}
	revision := configstore.Revision(before, present)
	agent := []byte(`{"delegationDepth":2,"automaticTitles":false}`)
	if err := configstore.Write(dir, configstore.Agent, agent); err != nil {
		t.Fatal(err)
	}
	if err := configstore.CompareAndSwap(dir, configstore.Server, revision, []byte(`{"port":5187}`)); err != nil {
		t.Fatalf("unrelated update caused a conflict: %v", err)
	}
	if err := configstore.CompareAndSwap(dir, configstore.Server, revision, []byte(`{"port":5188}`)); !errors.Is(err, configstore.ErrConflict) {
		t.Fatalf("stale revision was accepted: %v", err)
	}
	got, present, err := configstore.Read(dir, configstore.Agent)
	if err != nil || !present || !bytes.Equal(got, agent) {
		t.Fatalf("another section was lost: %s %v", got, err)
	}
}

func TestMissingCredentialDoesNotFallBackToLegacyFiles(t *testing.T) {
	dir := privateConfigDir(t)
	models := []byte(`{"models":[{"key":"example","provider":"openai","id":"example","apiKey":"old-key"}]}`)
	if err := configstore.Write(dir, configstore.Models, models); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configstore.KeyFilename), []byte(`{"version":1,"entries":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), models, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configstore.Read(dir, configstore.Models); err == nil {
		t.Fatal("missing credential silently fell back to a legacy secret")
	}
	if err := configstore.Migrate(dir); err == nil {
		t.Fatal("startup silently repaired a broken current reference with legacy data")
	}
}

func TestReadDetectsSameSizeAndTimestampEdits(t *testing.T) {
	dir := privateConfigDir(t)
	if err := configstore.Write(dir, configstore.Server, []byte(`{"port":5186}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configstore.Read(dir, configstore.Server); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, configstore.Filename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("5186"), []byte("5187"))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, present, err := configstore.Read(dir, configstore.Server)
	if err != nil || !present || !bytes.Contains(got, []byte("5187")) {
		t.Fatalf("stale cache after an external edit: %s %v", got, err)
	}
}
