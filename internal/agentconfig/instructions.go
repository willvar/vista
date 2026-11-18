package agentconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf8"

	"vista/internal/platform"
	"vista/internal/tools"
)

const filename = "AGENTS.md"

// InstructionsPath returns the global instructions file in Vista's configuration directory.
func InstructionsPath(dir string) string {
	return filepath.Join(dir, filename)
}

type InstructionSnapshot struct {
	Content string
	SHA256  string
}

func Read(dir string) (string, error) {
	snapshot, err := ReadSnapshot(dir)
	return snapshot.Content, err
}

func ReadSnapshot(dir string) (InstructionSnapshot, error) {
	data, err := os.ReadFile(InstructionsPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return InstructionSnapshot{SHA256: "missing"}, nil
	}
	if err != nil {
		return InstructionSnapshot{}, err
	}
	if err := validate(string(data)); err != nil {
		return InstructionSnapshot{}, err
	}
	hash := sha256.Sum256(data)
	return InstructionSnapshot{Content: string(data), SHA256: hex.EncodeToString(hash[:])}, nil
}

func WriteChecked(ctx context.Context, stateDir, dir, expectedSHA256, instructions string) (tools.FileWrite, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return tools.FileWrite{}, errors.New("Vista configuration directory must be private")
	}
	return tools.WriteTextWithDiff(ctx, stateDir, InstructionsPath(dir), expectedSHA256, instructions)
}

func Write(dir, instructions string) error {
	if err := validate(instructions); err != nil {
		return err
	}
	return writePrivateFile(dir, filename, []byte(instructions), instructions == "")
}

func writePrivateFile(dir, name string, data []byte, remove bool) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("Vista configuration directory must be private")
	}
	path := filepath.Join(dir, name)
	if remove {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	file, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
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

func validate(instructions string) error {
	if !utf8.ValidString(instructions) {
		return errors.New("agent instructions must be UTF-8")
	}
	return nil
}
