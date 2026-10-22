package opencodeconfig

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func Path(path, home string) (string, error) {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("OpenCode file paths must be absolute")
	}
	return filepath.Clean(path), nil
}

func ReadFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("OpenCode input must be a regular file of at most 4 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, errors.New("could not read OpenCode input")
	}
	return data, nil
}

var reference = regexp.MustCompile(`\{(env|file):([^{}]+)\}`)

func ResolveReferences(value any, directory, home string) (any, error) {
	switch value := value.(type) {
	case string:
		var failure error
		value = reference.ReplaceAllStringFunc(value, func(match string) string {
			parts := reference.FindStringSubmatch(match)
			if parts[1] == "env" {
				resolved, exists := os.LookupEnv(parts[2])
				if !exists {
					failure = errors.New("environment reference is unavailable")
				}
				return resolved
			}
			path := parts[2]
			if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
				path = filepath.Join(home, path[2:])
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(directory, path)
			}
			data, err := ReadFile(path)
			if err != nil {
				failure = err
			}
			return strings.TrimSpace(string(data))
		})
		return value, failure
	case map[string]any:
		for key, child := range value {
			resolved, err := ResolveReferences(child, directory, home)
			if err != nil {
				return nil, err
			}
			value[key] = resolved
		}
	case []any:
		for i, child := range value {
			resolved, err := ResolveReferences(child, directory, home)
			if err != nil {
				return nil, err
			}
			value[i] = resolved
		}
	}
	return value, nil
}

func Merge(before, after any) any {
	left, leftOK := before.(map[string]any)
	right, rightOK := after.(map[string]any)
	if !leftOK || !rightOK {
		return after
	}
	for key, value := range right {
		left[key] = Merge(left[key], value)
	}
	return left
}

// Remove comments and trailing commas without changing quoted strings or
// offsets. encoding/json remains responsible for all syntax validation.
func JSON(data []byte) []byte {
	data = slices.Clone(data)
	inString := false
	for i := 0; i < len(data); i++ {
		if inString {
			if data[i] == '\\' {
				i++
			} else if data[i] == '"' {
				inString = false
			}
			continue
		}
		if data[i] == '"' {
			inString = true
			continue
		}
		if data[i] != '/' || i+1 >= len(data) {
			continue
		}
		switch data[i+1] {
		case '/':
			for i < len(data) && data[i] != '\n' {
				data[i] = ' '
				i++
			}
		case '*':
			start, end := i, bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return []byte("invalid unterminated comment")
			}
			i += end + 3
			for j := start; j <= i; j++ {
				if data[j] != '\n' && data[j] != '\r' {
					data[j] = ' '
				}
			}
		}
	}
	inString = false
	for i := 0; i < len(data); i++ {
		if inString {
			if data[i] == '\\' {
				i++
			} else if data[i] == '"' {
				inString = false
			}
			continue
		}
		if data[i] == '"' {
			inString = true
			continue
		}
		if data[i] == ',' {
			previous := i - 1
			for previous >= 0 && strings.ContainsRune(" \t\r\n", rune(data[previous])) {
				previous--
			}
			if previous < 0 || strings.ContainsRune("{[:,", rune(data[previous])) {
				continue
			}
			j := i + 1
			for j < len(data) && strings.ContainsRune(" \t\r\n", rune(data[j])) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				data[i] = ' '
			}
		}
	}
	return data
}
