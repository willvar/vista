// Package appdirs resolves Vista directories using the operating system's
// standard user configuration directory.
package appdirs

import (
	"errors"
	"os"
	"path/filepath"
)

// BindWorkerConfigDir is called once by the hidden Worker command before it
// can load settings. The serving process always keeps the default directory.
var workerConfigDir string

func BindWorkerConfigDir(dir string) error {
	if workerConfigDir != "" || !filepath.IsAbs(dir) {
		return errors.New("invalid Worker configuration directory")
	}
	workerConfigDir = filepath.Clean(dir)
	return nil
}

// Config contains settings and user-authored instructions.
func Config() (string, error) {
	if workerConfigDir != "" {
		return workerConfigDir, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return configDir(root)
}

// State contains runtime data beneath the Vista configuration directory.
func State() (string, error) {
	config, err := Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "state"), nil
}

func configDir(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("user configuration directory must be absolute")
	}
	return filepath.Join(root, "vista"), nil
}
