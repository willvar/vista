package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"vista/internal/modelcatalog"
	"vista/internal/opencodeconfig"
	"vista/internal/tools"
)

func readOpenCodeSkills(ctx context.Context, input extensionImportInput) (extensionImportPreview, error) {
	source, result, fingerprint, err := readExtensionSource(input)
	if err != nil {
		return result, err
	}
	roots := []string{}
	for _, directory := range source.Directories {
		roots = append(roots, filepath.Join(directory, "skill"), filepath.Join(directory, "skills"))
	}
	for _, document := range source.Documents {
		if value, exists := document.Value["skills"]; exists {
			items, err := opencodeconfig.Skills(value)
			if err != nil {
				return result, err
			}
			for _, item := range items {
				resolved, err := opencodeconfig.ResolveReferences(item, filepath.Dir(document.Path), source.Home)
				path, ok := resolved.(string)
				if err != nil || !ok {
					return result, errors.New("could not resolve an OpenCode skill source")
				}
				if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
					id := sha256.Sum256([]byte(path))
					result.Entries = append(result.Entries, extensionImportEntry{ID: hex.EncodeToString(id[:]), Name: "Remote skill source", Reason: "remote"})
					continue
				}
				if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
					path = filepath.Join(source.Home, path[2:])
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(result.Directory, path)
				}
				roots = append(roots, filepath.Clean(path))
			}
		}
	}
	seen := map[string]bool{}
	var scan func(string, int) error
	scan = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("could not read skill directory: %s", path)
		}
		if seen[resolved] {
			return nil
		}
		seen[resolved] = true
		files, err := os.ReadDir(resolved)
		if err != nil {
			return fmt.Errorf("could not read skill directory: %s", path)
		}
		if slices.ContainsFunc(files, func(file os.DirEntry) bool { return file.Name() == "SKILL.md" }) {
			id := sha256.Sum256([]byte(path))
			folder := filepath.Base(path)
			entry := extensionImportEntry{ID: hex.EncodeToString(id[:]), Name: folder, Location: path, skillRoot: resolved, skillFolder: folder}
			var content []byte
			root, readErr := os.OpenRoot(resolved)
			if readErr == nil {
				content, readErr = root.ReadFile("SKILL.md")
				root.Close()
			}
			name, description, metadataErr := tools.SkillMetadata(string(content))
			if !validSkillPath(folder) || strings.HasPrefix(folder, ".") || readErr != nil || metadataErr != nil {
				entry.Reason = "invalid"
			} else {
				entry.Name, entry.Description = name, description
				entry.skillDigest, entry.FileCount, entry.Bytes, err = copySkillFolder(ctx, resolved, nil)
				if err != nil {
					entry.Reason = "files"
				}
				_, markerErr := os.Lstat(filepath.Join(resolved, tools.SkillDisabledFile))
				entry.Disabled = markerErr == nil
			}
			result.Entries = append(result.Entries, entry)
			return ctx.Err()
		}
		for _, file := range files {
			if strings.HasPrefix(file.Name(), ".") || file.Name() == "node_modules" {
				continue
			}
			if !file.IsDir() && file.Type()&os.ModeSymlink == 0 {
				if depth == 0 && strings.HasSuffix(file.Name(), ".md") {
					id := sha256.Sum256([]byte(filepath.Join(path, file.Name())))
					result.Entries = append(result.Entries, extensionImportEntry{ID: hex.EncodeToString(id[:]), Name: file.Name(), Location: path, Reason: "format"})
				}
				continue
			}
			if depth < 6 {
				child := filepath.Join(path, file.Name())
				if info, err := os.Stat(child); err == nil && info.IsDir() {
					if err := scan(child, depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := scan(root, 0); err != nil {
			return result, err
		}
	}
	// OpenCode's later sources take precedence when folder IDs collide.
	destinations := map[string]bool{}
	for i := len(result.Entries) - 1; i >= 0; i-- {
		entry := &result.Entries[i]
		if entry.Reason == "" {
			key := strings.ToLower(entry.skillFolder)
			if destinations[key] {
				entry.Reason = "duplicate"
			}
			destinations[key] = true
		}
	}
	return finishExtensionPreview(result, fingerprint), nil
}

type importReader struct {
	ctx context.Context
	io.Reader
}

func (r importReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

// Hash every file, including binary assets. A destination means copy into an
// already reserved empty folder; exclusive creates never replace its files.
func copySkillFolder(ctx context.Context, directory string, destination *os.Root) (string, int, int64, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", 0, 0, err
	}
	defer root.Close()
	digest := sha256.New()
	count, total := 0, int64(0)
	paths := []string{}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return ctx.Err()
		}
		if !validSkillPath(path) {
			return errors.New("skill contains an unsupported file name")
		}
		if entry.IsDir() {
			if destination != nil {
				if err := destination.Mkdir(path, 0700); err != nil {
					return err
				}
			}
			_, _ = fmt.Fprintf(digest, "directory %q\n", path)
			return ctx.Err()
		}
		paths = append(paths, path)
		return ctx.Err()
	})
	// Publish the entrypoint after its supporting files.
	slices.SortStableFunc(paths, func(a, b string) int {
		if a == b {
			return 0
		}
		if a == "SKILL.md" {
			return 1
		}
		if b == "SKILL.md" {
			return -1
		}
		return strings.Compare(a, b)
	})
	for _, path := range paths {
		if err != nil {
			break
		}
		info, statErr := root.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			err = errors.New("skill contains an unreadable file or directory link")
			break
		}
		func() {
			file, openErr := root.Open(path)
			if openErr != nil {
				err = openErr
				return
			}
			defer file.Close()
			fileHash := sha256.New()
			var writer io.Writer = fileHash
			if destination != nil {
				output, createErr := destination.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|info.Mode().Perm()&0111)
				if createErr != nil {
					err = createErr
					return
				}
				defer func() {
					if closeErr := output.Close(); err == nil {
						err = closeErr
					}
				}()
				writer = io.MultiWriter(writer, output)
			}
			var size int64
			size, err = io.Copy(writer, importReader{ctx, file})
			_, _ = fmt.Fprintf(digest, "file %q %d %d %x\n", path, size, info.Mode().Perm()&0111, fileHash.Sum(nil))
			count++
			total += size
		}()
	}
	return hex.EncodeToString(digest.Sum(nil)), count, total, err
}

func (a *App) copyImportedSkills(ctx context.Context, entries []extensionImportEntry) error {
	root, err := a.openSkillsRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	stage := ".vista-import-" + uuid.NewString()
	if err := root.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer root.RemoveAll(stage)
	staging, err := root.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer staging.Close()
	for _, entry := range entries {
		if err := staging.Mkdir(entry.skillFolder, 0700); err != nil {
			return err
		}
		folder, err := staging.OpenRoot(entry.skillFolder)
		if err != nil {
			return err
		}
		digest, _, _, copyErr := copySkillFolder(ctx, entry.skillRoot, folder)
		folder.Close()
		if copyErr != nil {
			return errors.New("could not copy the complete skill folder: " + entry.Name)
		}
		if digest != entry.skillDigest {
			return modelcatalog.NewSettingsError("settings_conflict", "Skill files changed while importing. Read them again.", nil)
		}
	}
	created := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, folder := range created {
				_ = root.RemoveAll(folder)
			}
		}
	}()
	// Reserve destinations exclusively before publishing any entrypoints.
	for _, entry := range entries {
		if err := root.Mkdir(entry.skillFolder, 0700); err != nil {
			return modelcatalog.NewSettingsError("settings_conflict", "A destination skill already exists or could not be created. Read it again.", nil)
		}
		created = append(created, entry.skillFolder)
	}
	for _, entry := range entries {
		folder, err := root.OpenRoot(entry.skillFolder)
		if err != nil {
			return err
		}
		_, _, _, err = copySkillFolder(ctx, filepath.Join(root.Name(), stage, entry.skillFolder), folder)
		folder.Close()
		if err != nil {
			return errors.New("could not save the complete skill folder: " + entry.Name)
		}
	}
	committed = true
	return nil
}
