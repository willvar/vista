// Package appdirs resolves Vista directories using the operating system's
// standard user configuration directory.
package appdirs

import (
	"errors"
	"os"
	"path/filepath"
)

// Config contains settings and user-authored instructions.
func Config() (string, error) {
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
