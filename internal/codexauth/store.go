package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"vista/internal/platform"
)

func Path(dir, connectionID string) string {
	if uuid.Validate(connectionID) != nil {
		return ""
	}
	return filepath.Join(dir, "codex-auth", connectionID+".json")
}

func (v Credential) valid() bool {
	return v.AccessToken != "" && v.RefreshToken != "" && v.AccountID != "" && !v.ExpiresAt.IsZero() && len(v.AccessToken) <= 16<<10 && len(v.RefreshToken) <= 16<<10 && len(v.AccountID) <= 256
}

// All Vista processes use the same advisory lock. Rotating a refresh token is
// a read/refresh/write transaction; process-local mutexes cannot protect it.
func lock(ctx context.Context, path string) (*os.File, error) {
	if path == "" {
		return nil, errors.New("invalid Codex connection")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err = platform.TryLock(file)
		if err == nil {
			return file, nil
		}
		if !platform.IsLockBusy(err) {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func read(path string) (Credential, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Credential{}, errors.New("Codex credential is unavailable; reconnect OpenAI Codex in Settings")
	}
	if !info.Mode().IsRegular() || !platform.PrivateMode(info.Mode()) || info.Size() > 40<<10 {
		return Credential{}, errors.New("invalid Codex credential file")
	}
	data, err := os.ReadFile(path)
	var credential Credential
	if err != nil || json.Unmarshal(data, &credential) != nil || !credential.valid() {
		return Credential{}, errors.New("invalid Codex credential; reconnect OpenAI Codex in Settings")
	}
	return credential, nil
}

func write(path string, credential Credential) error {
	if !credential.valid() {
		return errors.New("invalid Codex credential")
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func Save(ctx context.Context, path string, credential Credential) error {
	guard, err := lock(ctx, path)
	if err != nil {
		return err
	}
	defer guard.Close()
	return write(path, credential)
}

func Read(ctx context.Context, path string) (Credential, error) {
	guard, err := lock(ctx, path)
	if err != nil {
		return Credential{}, err
	}
	defer guard.Close()
	return read(path)
}

func Remove(ctx context.Context, path string) error {
	guard, err := lock(ctx, path)
	if err != nil {
		return err
	}
	defer guard.Close()
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Token reads the current token for every request, refreshing shortly before
// expiry. rejected is set only after an HTTP 401, and avoids refreshing twice
// when another worker already replaced the rejected token.
func (c *Client) Token(ctx context.Context, path, rejected string) (Credential, error) {
	guard, err := lock(ctx, path)
	if err != nil {
		return Credential{}, err
	}
	defer guard.Close()
	credential, err := read(path)
	if err != nil {
		return Credential{}, err
	}
	if time.Until(credential.ExpiresAt) > time.Minute && (rejected == "" || credential.AccessToken != rejected) {
		return credential, nil
	}
	updated, err := c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {credential.RefreshToken}}, credential.RefreshToken)
	if err != nil {
		return Credential{}, err
	}
	if updated.AccountID != credential.AccountID {
		return Credential{}, errors.New("Codex account changed during refresh; reconnect OpenAI Codex in Settings")
	}
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	if err := write(path, updated); err != nil {
		return Credential{}, err
	}
	return updated, nil
}
