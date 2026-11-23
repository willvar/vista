package resources

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/configstore"
)

const LocalBackend = "local"

// BackendConfig is an immutable storage identity. Only access credentials may
// change in place; master keys and object locations remain bound to the ID.
// Secrets are logical configstore fields, split into private key.json entries.
type BackendConfig struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Endpoint     string `json:"endpoint,omitempty"`
	Bucket       string `json:"bucket,omitempty"`
	Region       string `json:"region,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
	AccessKey    string `json:"accessKey,omitempty"`
	SecretKey    string `json:"secretKey,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
	MasterKey    string `json:"masterKey,omitempty"`
	// Unused backends have never been selected for a migration. Only a
	// durable check intent can require inventory before then.
	Unused bool `json:"unused,omitempty"`
}

const MaxPresets = 16

// Preset is a selectable name. Physical backends outlive the names that point
// to them so that previous copies and interrupted checks remain recoverable.
type Preset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BackendID string `json:"backendId"`
}

type Settings struct {
	Backends []BackendConfig `json:"backends"`
	Presets  []Preset        `json:"presets"` // nil means legacy; an empty list does not re-import.
}

func ValidPresetName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && utf8.RuneCountInString(name) <= 128 && !strings.ContainsAny(name, "\x00\r\n")
}

func (l Library) NeedsInventory(backend BackendConfig) (bool, error) {
	if !backend.Unused {
		return true, nil
	}
	entries, err := os.ReadDir(filepath.Join(l.StateDir, "resource-backends", backend.ID, "uploads"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return len(entries) != 0, err
}

func LoadSettings(configDir string) (Settings, error) {
	data, present, err := configstore.Read(configDir, configstore.Storage)
	if err != nil {
		return Settings{}, err
	}
	if !present {
		data = nil
	}
	return DecodeSettings(data)
}

func DecodeSettings(data []byte) (Settings, error) {
	if len(data) == 0 {
		return Settings{Backends: []BackendConfig{}}, nil
	}
	var result Settings
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return result, errors.New("invalid resource storage configuration")
	}
	seen := map[string]bool{}
	for _, backend := range result.Backends {
		if seen[backend.ID] {
			return result, errors.New("duplicate resource storage identity")
		}
		seen[backend.ID] = true
		if err := backend.Validate(); err != nil {
			return result, err
		}
	}
	if len(result.Presets) > MaxPresets {
		return result, errors.New("too many storage configurations")
	}
	for i, preset := range result.Presets {
		id, err := uuid.Parse(preset.ID)
		if err != nil || id.String() != preset.ID || !seen[preset.BackendID] || !ValidPresetName(preset.Name) {
			return result, errors.New("invalid storage configuration")
		}
		for _, earlier := range result.Presets[:i] {
			if earlier.ID == preset.ID || earlier.BackendID == preset.BackendID || strings.EqualFold(earlier.Name, preset.Name) {
				return result, errors.New("duplicate storage configuration")
			}
		}
	}
	return result, nil
}

func (s Settings) Backend(id string) (BackendConfig, error) {
	if id == LocalBackend {
		return BackendConfig{ID: LocalBackend, Kind: "local"}, nil
	}
	for _, backend := range s.Backends {
		if backend.ID == id {
			return backend, nil
		}
	}
	return BackendConfig{}, errors.New("resource storage configuration is unavailable")
}

func (b BackendConfig) Validate() error {
	if b.ID == LocalBackend && b.Kind == "local" {
		return nil
	}
	id, parseErr := uuid.Parse(b.ID)
	if parseErr != nil || id.String() != b.ID || b.Kind != "s3" {
		return errors.New("invalid resource storage identity")
	}
	for _, c := range b.ID {
		if !strings.ContainsRune("0123456789abcdef-", c) {
			return errors.New("invalid resource storage identity")
		}
	}
	u, err := url.Parse(b.Endpoint)
	if err != nil || len(b.Endpoint) > 2048 || len(b.Region) > 255 || strings.ContainsAny(b.Region, "\r\n") || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("storage endpoint must be an HTTP(S) origin without credentials or a path")
	}
	if b.Bucket == "" || strings.ContainsAny(b.Bucket, "/\\\x00\r\n ") || len(b.Bucket) > 255 {
		return errors.New("invalid storage bucket")
	}
	if len(b.Prefix) > 1024 || strings.ContainsAny(b.Prefix, "\\\x00\r\n") || strings.Trim(b.Prefix, "/") != b.Prefix || (b.Prefix != "" && (path.Clean(b.Prefix) != b.Prefix || b.Prefix == "." || b.Prefix == ".." || strings.HasPrefix(b.Prefix, "../"))) {
		return errors.New("invalid storage prefix")
	}
	if b.AccessKey == "" || b.SecretKey == "" || len(b.AccessKey) > 4096 || len(b.SecretKey) > 4096 || len(b.SessionToken) > 16384 {
		return errors.New("storage access credentials are required")
	}
	key, err := hex.DecodeString(b.MasterKey)
	if err != nil || len(key) != 32 {
		return errors.New("resource encryption key is unavailable")
	}
	for i := range key {
		key[i] = 0
	}
	return nil
}

func (b BackendConfig) SameLocation(other BackendConfig) bool {
	return b.Kind == other.Kind && b.Endpoint == other.Endpoint && b.Bucket == other.Bucket && b.Prefix == other.Prefix
}

// PublicConfig is the only configuration projection sent over HTTP/SSE. Saved
// credentials and the application-managed encryption key are never returned.
type PublicConfig struct {
	Kind           string `json:"kind"`
	Endpoint       string `json:"endpoint,omitempty"`
	Bucket         string `json:"bucket,omitempty"`
	Region         string `json:"region,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
	HasCredentials bool   `json:"hasCredentials"`
}

func (b BackendConfig) Public() PublicConfig {
	return PublicConfig{Kind: b.Kind, Endpoint: b.Endpoint, Bucket: b.Bucket, Region: b.Region, Prefix: b.Prefix, HasCredentials: b.AccessKey != "" && b.SecretKey != ""}
}
