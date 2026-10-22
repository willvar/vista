package modelcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"vista/internal/opencodeconfig"
)

type openCodeCredentialStore struct {
	Path   string
	Values map[string]json.RawMessage
}

func readOpenCodeCredentialStores(ctx context.Context, source opencodeconfig.Source, path string) ([]openCodeCredentialStore, error) {
	explicit := path != ""
	paths := []string{filepath.Join(source.DataDir, "auth.json"), filepath.Join(source.DataDir, "opencode.db")}
	if explicit {
		resolved, err := opencodeconfig.Path(path, source.Home)
		if err != nil {
			return nil, err
		}
		paths = []string{resolved}
	}
	stores := []openCodeCredentialStore{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) && !explicit {
			continue
		}
		if err != nil {
			return nil, errors.New("could not read OpenCode credentials file")
		}
		info, statErr := file.Stat()
		var header [16]byte
		_, readErr := io.ReadFull(file, header[:])
		file.Close()
		if statErr != nil || !info.Mode().IsRegular() || readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return nil, errors.New("could not read OpenCode credentials file")
		}
		legacy := string(header[:]) != "SQLite format 3\x00"
		var values map[string]json.RawMessage
		if legacy {
			data, err := opencodeconfig.ReadFile(path)
			if err != nil || json.Unmarshal(data, &values) != nil || values == nil || len(values) > 4096 {
				return nil, errors.New("invalid or oversized OpenCode credentials file")
			}
		} else {
			values, err = readOpenCodeCredentials(ctx, path, explicit)
			if err != nil {
				return nil, err
			}
			if values == nil {
				continue // V1 also has a session database, without a credential table.
			}
		}
		normalized := map[string]json.RawMessage{}
		for id, value := range values {
			if len(value) > 64<<10 {
				return nil, errors.New("oversized OpenCode credential")
			}
			target := opencodeconfig.ProviderID(id)
			if _, exists := normalized[target]; exists && target != id {
				continue // The canonical provider wins over a retired alias.
			}
			normalized[target] = normalizeOpenCodeCredential(id, value, legacy)
		}
		stores = append(stores, openCodeCredentialStore{Path: path, Values: normalized})
	}
	return stores, nil
}

func normalizeOpenCodeCredential(id string, value json.RawMessage, legacy bool) json.RawMessage {
	value = bytes.TrimSpace(value)
	var auth openCodeAuth
	if len(value) == 0 || value[0] != '{' || json.Unmarshal(value, &auth) != nil {
		return value
	}
	if legacy {
		var fields struct {
			AccountID     string `json:"accountId"`
			EnterpriseURL string `json:"enterpriseUrl"`
		}
		if json.Unmarshal(value, &fields) != nil {
			return value
		}
		if auth.Type == "api" {
			auth.Type = "key"
		}
		if auth.Type == "oauth" {
			if auth.MethodID == "" {
				switch id {
				case "openai":
					auth.MethodID = "chatgpt-browser"
				case "github-copilot":
					auth.MethodID = "device"
				}
			}
			if auth.Metadata.AccountID == "" {
				auth.Metadata.AccountID = fields.AccountID
			}
			if auth.Metadata.EnterpriseURL == "" {
				auth.Metadata.EnterpriseURL = fields.EnterpriseURL
			}
		}
	}
	encoded, _ := json.Marshal(auth)
	return encoded
}
