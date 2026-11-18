package configstore_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"vista/internal/configstore"
	"vista/internal/platform"
)

func TestStorageCredentialsUseStablePrivateOwners(t *testing.T) {
	dir := privateConfigDir(t)
	first := map[string]any{"id": "3ee01325-58e1-48c2-b86c-dae106c47102", "kind": "s3", "endpoint": "https://storage.example", "bucket": "resources", "accessKey": "test-access", "secretKey": "test-secret", "masterKey": "test-master"}
	write := func(backends ...map[string]any) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"backends": backends})
		if err := configstore.Write(dir, configstore.Storage, data); err != nil {
			t.Fatal(err)
		}
	}
	write(first)
	main, err := os.ReadFile(filepath.Join(dir, configstore.Filename))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := os.ReadFile(filepath.Join(dir, configstore.KeyFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"test-access", "test-secret", "test-master"} {
		if bytes.Contains(main, []byte(secret)) || !bytes.Contains(keys, []byte(secret)) {
			t.Fatal("storage credentials escaped their private document")
		}
	}
	resolved, present, err := configstore.Read(dir, configstore.Storage)
	if err != nil || !present || !bytes.Contains(resolved, []byte("test-master")) {
		t.Fatal("storage credentials were not resolved", err)
	}
	second := map[string]any{"id": "7caef611-c79a-433d-84ae-6493de27bc03", "kind": "s3", "secretKey": "another-secret", "masterKey": "another-master"}
	write(first, second)
	if err := configstore.Write(dir, configstore.Agent, []byte(`{"automaticTitles":false}`)); err != nil {
		t.Fatal(err)
	}
	resolved, _, err = configstore.Read(dir, configstore.Storage)
	if err != nil || !bytes.Contains(resolved, []byte("test-master")) || !bytes.Contains(resolved, []byte("another-master")) {
		t.Fatal("retained source credentials were lost", err)
	}
	for _, name := range []string{configstore.Filename, configstore.KeyFilename} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !platform.PrivateMode(info.Mode()) {
			t.Fatal("private storage configuration permissions changed", err)
		}
	}
}
