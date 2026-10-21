package agentconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"vista/internal/platform"
)

const MaxInstructionsBytes = 4096

const filename = "AGENTS.md"

// InstructionsPath returns the global instructions file in Vista's configuration directory.
func InstructionsPath(dir string) string {
	return filepath.Join(dir, filename)
}

func Read(dir string) (string, error) {
	file, err := os.Open(InstructionsPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxInstructionsBytes+1))
	if err != nil {
		return "", err
	}
	if err := validate(string(data)); err != nil {
		return "", err
	}
	return string(data), nil
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
	if len(instructions) > MaxInstructionsBytes {
		return fmt.Errorf("agent instructions exceed %d bytes", MaxInstructionsBytes)
	}
	if !utf8.ValidString(instructions) {
		return errors.New("agent instructions must be UTF-8")
	}
	return nil
}
