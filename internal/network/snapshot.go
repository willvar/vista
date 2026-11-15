package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

type settingsKey struct{}

// WithOperation creates a demand-driven snapshot for one independent operation.
// Configuration is read only at its first policy selection; concurrent and
// nested selections share that result, including a load failure.
func WithOperation(ctx context.Context) context.Context {
	if ctx.Value(settingsKey{}) != nil {
		return ctx
	}
	dir := configDirectory(ctx)
	load := sync.OnceValues(func() (Settings, error) {
		if dir == "" {
			return Defaults(), nil
		}
		return Load(dir)
	})
	return context.WithValue(ctx, settingsKey{}, load)
}

func contextSettings(ctx context.Context) (Settings, bool, error) {
	switch value := ctx.Value(settingsKey{}).(type) {
	case Settings:
		return value, true, nil
	case func() (Settings, error):
		settings, err := value()
		return settings, true, err
	default:
		return Settings{}, false, nil
	}
}

// WithSettings holds a private request snapshot so choosing another connection
// cannot reload a different profile generation during the same operation.
func WithSettings(ctx context.Context, settings Settings) context.Context {
	return context.WithValue(ctx, settingsKey{}, settings)
}

// Fingerprint identifies functional routing, excluding labels.
// It is for internal pool identity, not a public credential projection.
func Fingerprint(policy Policy) string {
	if policy.Profile != nil {
		profile := *policy.Profile
		profile.Name = ""
		policy.Profile = &profile
	}
	data, _ := json.Marshal(policy)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func HasPolicy(ctx context.Context) bool { _, ok := ctx.Value(policyKey{}).(Policy); return ok }
