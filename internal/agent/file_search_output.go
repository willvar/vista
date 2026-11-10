package agent

import (
	"path/filepath"

	"vista/internal/tools"
)

// Keep the filesystem API's absolute coordinates internally. Tool results use
// one root and file groups so later model requests do not repeat long paths on
// every hit and context line. No source lines or search metadata are removed.
type searchLine struct {
	Line    int    `json:"line"`
	Text    string `json:"text"`
	Context bool   `json:"context,omitempty"`
}

type searchFile struct {
	Path    string       `json:"path"`
	Matches []searchLine `json:"matches"`
}

type searchOutput struct {
	Root         string       `json:"root"`
	Files        []searchFile `json:"files"`
	Paths        []string     `json:"paths,omitempty"`
	Count        int          `json:"count"`
	Truncated    bool         `json:"truncated"`
	FilesScanned int          `json:"filesScanned"`
	tools.SearchOmissions
}

func relativeSearchPath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func relativeSearchPaths(root string, paths []string) []string {
	result := make([]string, len(paths))
	for i, path := range paths {
		result[i] = relativeSearchPath(root, path)
	}
	return result
}

func marshalSearchOutput(root string, result tools.SearchResult) (string, error) {
	root = filepath.Clean(root)
	output := searchOutput{Root: root, Files: []searchFile{}, Paths: relativeSearchPaths(root, result.Paths),
		Count: result.Count, Truncated: result.Truncated, FilesScanned: result.FilesScanned, SearchOmissions: result.SearchOmissions}
	groups := make(map[string]int)
	for _, match := range result.Matches {
		index, ok := groups[match.Path]
		if !ok {
			index = len(output.Files)
			groups[match.Path] = index
			output.Files = append(output.Files, searchFile{Path: relativeSearchPath(root, match.Path)})
		}
		output.Files[index].Matches = append(output.Files[index].Matches, searchLine{Line: match.Line, Text: match.Text, Context: match.Context})
	}
	return marshalResult(output)
}

func marshalFindFilesOutput(root string, result tools.FindFilesResult) (string, error) {
	root = filepath.Clean(root)
	return marshalResult(struct {
		Root      string   `json:"root"`
		Paths     []string `json:"paths"`
		Truncated bool     `json:"truncated"`
		tools.SearchOmissions
	}{root, relativeSearchPaths(root, result.Paths), result.Truncated, result.SearchOmissions})
}
