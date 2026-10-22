// Package opencodeconfig reads declarative OpenCode sources without loading
// plugins, executing commands, fetching remote configuration, or changing files.
package opencodeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type Document struct {
	Path  string
	Data  []byte
	Value map[string]any
}

type Source struct {
	Home        string
	DataDir     string
	Directories []string
	Documents   []Document
}

func Read(configPath string) (Source, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Source{}, errors.New("could not locate the OpenCode home directory")
	}
	configRoot := os.Getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		configRoot = filepath.Join(home, ".config")
	}
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(configRoot, "opencode")
	result := Source{Home: home, DataDir: filepath.Join(dataRoot, "opencode"), Directories: []string{dir}, Documents: []Document{}}
	paths := []string{filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")}
	if override := os.Getenv("OPENCODE_CONFIG"); override != "" {
		paths = append(paths, override)
	}
	if override := os.Getenv("OPENCODE_CONFIG_DIR"); override != "" {
		paths = append(paths, filepath.Join(override, "opencode.json"), filepath.Join(override, "opencode.jsonc"))
		result.Directories = append(result.Directories, override)
	}
	if configPath != "" {
		paths, result.Directories = []string{configPath}, []string{filepath.Dir(configPath)}
	}
	for i, directory := range result.Directories {
		result.Directories[i], err = Path(directory, home)
		if err != nil {
			return Source{}, err
		}
	}
	seen := []string{}
	for _, path := range paths {
		path, err = Path(path, home)
		if err != nil {
			return Source{}, err
		}
		if slices.Contains(seen, path) {
			continue
		}
		seen = append(seen, path)
		data, err := ReadFile(path)
		if errors.Is(err, os.ErrNotExist) && configPath == "" {
			continue
		}
		if err != nil {
			return Source{}, fmt.Errorf("could not read OpenCode configuration: %s", path)
		}
		var value map[string]any
		if json.Unmarshal(JSON(data), &value) != nil || value == nil {
			return Source{}, fmt.Errorf("invalid OpenCode configuration: %s", path)
		}
		result.Documents = append(result.Documents, Document{Path: path, Data: data, Value: value})
	}
	return result, nil
}
