package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

type Match struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Text    string `json:"text"`
	Context bool   `json:"context,omitempty"`
}

type SearchResult struct {
	Matches      []Match  `json:"matches"`
	Paths        []string `json:"paths,omitempty"`
	Count        int      `json:"count"`
	Truncated    bool     `json:"truncated"`
	FilesScanned int      `json:"filesScanned"`
	SearchOmissions
}

// A partial search must not silently look like a complete negative result.
// Keep diagnostics bounded even when a broad root has many unreadable trees.
type SearchOmissions struct {
	SkippedDirectories int             `json:"skippedDirectories,omitempty"`
	Warnings           []SearchWarning `json:"warnings,omitempty"`
}

type SearchWarning struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

func (s *SearchOmissions) skip(path string, err error) {
	s.SkippedDirectories++
	if len(s.Warnings) < 20 {
		s.Warnings = append(s.Warnings, SearchWarning{Path: path, Error: err.Error()})
	}
}

type SearchOptions struct {
	Root         string   `json:"root"`
	Pattern      string   `json:"pattern"`
	Include      []string `json:"include,omitempty"`
	Exclude      []string `json:"exclude,omitempty"`
	Literal      bool     `json:"literal,omitempty"`
	IgnoreCase   bool     `json:"ignoreCase,omitempty"`
	ContextLines int      `json:"contextLines,omitempty"`
	Mode         string   `json:"mode,omitempty"`
}

func validateGlobs(patterns []string) error {
	for _, pattern := range patterns {
		if !doublestar.ValidatePattern(pattern) {
			return errors.New("invalid glob pattern: " + pattern)
		}
	}
	return nil
}

func matchesGlobs(patterns []string, relative string) bool {
	for _, pattern := range patterns {
		target := relative
		if !strings.Contains(pattern, "/") {
			target = filepath.Base(relative)
		}
		if matched, _ := doublestar.Match(pattern, target); matched {
			return true
		}
	}
	return false
}

// Both discovery and content search use the same depth-first ignore scopes.
// The traversal callback is deliberately sequential: inherited .gitignore
// scope must not be shared between concurrent directory callbacks.
func walkSearchFiles(ctx context.Context, root string, include, exclude []string, omissions *SearchOmissions, visit func(string, fs.DirEntry) error) error {
	if err := validateGlobs(append(append([]string{}, include...), exclude...)); err != nil {
		return err
	}
	ignores, err := newSearchIgnore(root)
	if err != nil {
		return err
	}
	prefixes := searchDirectoryPrefixes(include)
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path != root && errors.Is(walkErr, fs.ErrPermission) {
				omissions.skip(path, walkErr)
				return filepath.SkipDir
			}
			return walkErr
		}
		for ignores.directory != path && ignores.directory != filepath.Dir(path) {
			ignores = ignores.parent
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() && !searchDirectoryMayMatch(prefixes, relative) {
			return filepath.SkipDir
		}
		if ignores.ignored(path, entry.IsDir()) || matchesGlobs(exclude, relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if ignores.directory != path {
				next, err := ignores.descend(path)
				if err != nil {
					if path != root && errors.Is(err, fs.ErrPermission) {
						// Never traverse a subtree without its ignore rules.
						omissions.skip(path, err)
						return filepath.SkipDir
					}
					return err
				}
				ignores = next
			}
			return nil
		}
		if len(include) != 0 && !matchesGlobs(include, relative) {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return visit(path, entry)
	})
}

// Only literal directory prefixes can rule a branch out. Basename globs and
// leading wildcards still match at every depth, with their existing semantics.
func searchDirectoryPrefixes(patterns []string) []string {
	var prefixes []string
	for _, pattern := range patterns {
		parts := strings.Split(pattern, "/")
		end := 0
		for _, part := range parts[:len(parts)-1] {
			if strings.ContainsAny(part, "*?[]{}\\") {
				break
			}
			end++
		}
		literal := strings.Join(parts[:end], "/")
		if literal == "" {
			return nil // This include can match anywhere; no branch can be pruned.
		}
		prefixes = append(prefixes, literal)
	}
	return prefixes
}

func searchDirectoryMayMatch(prefixes []string, directory string) bool {
	if directory == "." || len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if directory == prefix || strings.HasPrefix(prefix, directory+"/") || strings.HasPrefix(directory, prefix+"/") {
			return true
		}
	}
	return false
}

type FindFilesResult struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
	SearchOmissions
}

func FindFiles(ctx context.Context, root, pattern string, exclude []string, limit int) (FindFilesResult, error) {
	root, err := absolute(root)
	if err != nil {
		return FindFilesResult{}, err
	}
	if pattern == "" {
		return FindFilesResult{}, errors.New("a file path glob is required")
	}
	if limit == 0 {
		limit = maxSearchResults
	}
	if limit < 1 || limit > MaxToolResultLines {
		return FindFilesResult{}, errors.New("limit must be between 1 and 2000")
	}
	result := FindFilesResult{Paths: []string{}}
	err = walkSearchFiles(ctx, root, []string{pattern}, exclude, &result.SearchOmissions, func(path string, _ fs.DirEntry) error {
		if len(result.Paths) == limit {
			result.Truncated = true
			return fs.SkipAll
		}
		result.Paths = append(result.Paths, path)
		return nil
	})
	result.Truncated = result.Truncated || result.SkippedDirectories > 0
	return result, err
}

type searchMatcher struct {
	expression *regexp.Regexp
	literal    string
}

func textMatcher(pattern string, literal, ignoreCase bool) (searchMatcher, error) {
	if pattern == "" {
		return searchMatcher{}, errors.New("search pattern must not be empty")
	}
	plain := ""
	if literal {
		if !ignoreCase {
			plain = pattern
		}
		pattern = regexp.QuoteMeta(pattern)
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	return searchMatcher{expression: re, literal: plain}, err
}

func (m searchMatcher) matches(text string) bool {
	if m.literal != "" {
		return strings.Contains(text, m.literal)
	}
	return m.expression.MatchString(text)
}

func searchPreview(text string) string {
	if len(text) > 512 {
		text = text[:512]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

func SearchFiles(ctx context.Context, options SearchOptions) (SearchResult, error) {
	root, err := absolute(options.Root)
	if err != nil {
		return SearchResult{}, err
	}
	if options.Mode == "" {
		options.Mode = "content"
	}
	if options.Mode != "content" && options.Mode != "files" && options.Mode != "count" {
		return SearchResult{}, errors.New("mode must be content, files or count")
	}
	if options.ContextLines < 0 || options.ContextLines > MaxToolResultLines {
		return SearchResult{}, errors.New("contextLines must be between 0 and 2000")
	}
	match, err := textMatcher(options.Pattern, options.Literal, options.IgnoreCase)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Matches: []Match{}}
	var bytesRead int64
	err = walkSearchFiles(ctx, root, options.Include, options.Exclude, &result.SearchOmissions, func(path string, entry fs.DirEntry) error {
		if result.FilesScanned >= maxSearchFiles || bytesRead >= maxSearchBytes {
			result.Truncated = true
			return fs.SkipAll
		}
		file, err := os.Open(path)
		if err != nil {
			result.Truncated = true
			return nil
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			result.Truncated = true
			return nil
		}
		remaining := int64(maxSearchBytes) - bytesRead
		var previous []Match
		var selected []Match
		count, after, lastLine, line := 0, 0, 0, 1
		invalid, limited := false, false
		var offset, lineStart int64
		var prefix strings.Builder
		scanErr := scanText(ctx, io.LimitReader(file, remaining), func(fragment []byte, end bool) error {
			if bytes.IndexByte(fragment, 0) >= 0 {
				invalid = true
				return fs.SkipAll
			}
			offset += int64(len(fragment))
			if prefix.Len() < MaxOutputPageBytes {
				prefix.Write(fragment[:min(len(fragment), MaxOutputPageBytes-prefix.Len())])
			}
			if !end {
				return nil
			}
			if info.Size() > remaining && offset >= remaining {
				// A scan budget boundary is not the end of the source line.
				limited = true
				return fs.SkipAll
			}
			if lineStart >= info.Size() && prefix.Len() == 0 {
				return nil // scanText's synthetic final blank line is not a source line.
			}
			text := prefix.String()
			hit := match.matches(text)
			if offset-lineStart > int64(len(text)) {
				// Match complete long lines through ReaderAt, retaining only a bounded preview.
				hit = match.expression.MatchReader(bufio.NewReader(webContextReader{ctx, io.NewSectionReader(file, lineStart, offset-lineStart)}))
			}
			current := Match{Path: path, Line: line, Context: !hit}
			if options.Mode == "content" {
				current.Text = searchPreview(text)
			}
			if hit {
				count++
				if options.Mode == "content" {
					for _, before := range previous {
						if len(result.Matches)+len(selected) >= MaxToolResultLines-1 {
							break
						}
						if before.Line > lastLine {
							selected = append(selected, before)
							lastLine = before.Line
						}
					}
					selected = append(selected, current)
					lastLine = line
					after = options.ContextLines
				}
			} else if options.Mode == "content" && after > 0 {
				selected = append(selected, current)
				lastLine = line
				after--
			}
			if options.Mode == "content" && options.ContextLines > 0 {
				previous = append(previous, current)
				if len(previous) > options.ContextLines {
					previous = previous[1:]
				}
			}
			if options.Mode == "content" && (result.Count+count >= maxSearchResults || len(result.Matches)+len(selected) >= MaxToolResultLines) {
				limited = true
				return fs.SkipAll
			}
			line++
			offset++ // source delimiter; ReaderAt does not change the scan position.
			lineStart = offset
			prefix.Reset()
			return nil
		})
		position, _ := file.Seek(0, io.SeekCurrent)
		bytesRead += position
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(scanErr, io.ErrUnexpectedEOF) && info.Size() > remaining && position >= remaining {
			// UTF-8 split at the byte budget keeps previously completed matches.
			limited = true
		} else if errors.Is(scanErr, errNotUTF8Text) {
			invalid = true
		} else if scanErr != nil && !errors.Is(scanErr, fs.SkipAll) {
			result.Truncated = true
			return nil
		}
		result.FilesScanned++
		if invalid {
			return nil
		}
		result.Count += count
		if count > 0 && options.Mode == "files" {
			result.Paths = append(result.Paths, path)
		}
		if options.Mode == "content" {
			result.Matches = append(result.Matches, selected...)
		}
		if limited {
			result.Truncated = true
			return fs.SkipAll
		}
		if options.Mode == "files" && len(result.Paths) >= maxSearchResults {
			result.Truncated = true
			return fs.SkipAll
		}
		return nil
	})
	result.Truncated = result.Truncated || result.SkippedDirectories > 0
	return result, err
}
