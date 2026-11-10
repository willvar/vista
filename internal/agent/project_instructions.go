package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

type instructionRange struct{ start, end int }

type instructionRead struct {
	hash   string
	ranges []instructionRange
	total  int
	hasEnd bool
}

func (read instructionRead) complete() bool {
	return read.hasEnd && len(read.ranges) == 1 && read.ranges[0].start == 0 && read.ranges[0].end == read.total
}

// Each task owns its model's read coverage. The run loop rebuilds it from the
// context consumed by a successful model request, before starting its tools.
// Tool goroutines only inspect it, so a concurrent read cannot authorize a
// write from the same model turn. Summaries without the original pages and
// other tasks' reads cannot authorize an operation either.
type projectInstructionState struct {
	global tools.InstructionFile // The exact version loaded into the system prompt.
	read   map[string]instructionRead
	pages  []instructionPage
}

const inheritedRulesPrefix = "Inherited directory rules already read by the parent. Each rule applies only in the directory containing its file and descendants (copilot-instructions.md uses the parent of .github). Nearer rules override outer rules; current user requests take precedence. Full text and content hashes follow:\n"

type instructionNotice struct {
	Status       string                  `json:"status"`
	Executed     bool                    `json:"executed"`
	Target       string                  `json:"target"`
	Instructions []tools.InstructionFile `json:"instructions"`
	Pages        []instructionTextPage   `json:"pages,omitempty"`
	Message      string                  `json:"message"`
}

type instructionTextPage struct {
	Offset int `json:"offset"`
	tools.SkillPage
}

func (s *projectInstructionState) prepare(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct{ Path, Root, Directory string }
	var targets []string
	switch call.Function.Name {
	case "apply_patch":
		var patch tools.PatchInput
		if err := decodeArguments(call.Function.Arguments, &patch); err != nil {
			return "", err
		}
		var err error
		targets, err = tools.PatchPaths(patch)
		if err != nil {
			return "", err
		}
	case "read_file", "write_file", "search", "find_files", "shell", "start_background", "read_resource", "read_skill":
		if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
			return "", err
		}
	default:
		return "", nil
	}
	target := input.Path
	switch call.Function.Name {
	case "search", "find_files":
		target = input.Root
	case "shell", "start_background":
		target = input.Directory
	}
	if len(targets) == 0 {
		targets = []string{target}
	}
	var absoluteTargets []string
	for _, target := range targets {
		// Hash resources and embedded skills have no filesystem target.
		if filepath.IsAbs(target) {
			absoluteTargets = append(absoluteTargets, filepath.Clean(target))
		}
	}
	files, err := tools.DiscoverProjectInstructions(ctx, absoluteTargets...)
	if err != nil {
		return "", err
	}
	var unread []tools.InstructionFile
	for _, file := range files {
		if file.Path == s.global.Path && file.SHA256 == s.global.SHA256 {
			continue
		}
		read := s.read[file.Path]
		if read.hash != file.SHA256 || !read.complete() {
			unread = append(unread, file)
		}
	}
	if target == "" && len(targets) > 0 {
		target = targets[0]
	}
	if len(unread) == 0 {
		return "", nil
	}
	notice := instructionNotice{
		Status: "instructions_required", Target: target, Instructions: unread,
		Message: "Operation not executed. Apply the rule text in pages within each file's directory scope. Read any remaining pages with read_project_instruction at nextOffset (or offset 0 if no page is included), then retry or revise this operation. Do not rediscover these same paths.",
	}
	for _, file := range unread {
		offset := 0
		if read := s.read[file.Path]; read.hash == file.SHA256 && len(read.ranges) > 0 && read.ranges[0].start == 0 {
			offset = read.ranges[0].end
		}
		page, err := tools.ReadProjectInstruction(file.Path, offset)
		if err != nil {
			return "", err
		}
		if page.SHA256 != file.SHA256 {
			return "", fmt.Errorf("project instructions changed while reading %s; retry the operation", file.Path)
		}
		if !notice.addPage(page, offset) {
			break
		}
	}
	return marshalResult(notice)
}

// Only complete tool pages actually present in the request acknowledge text.
// Discovery metadata, previews, skipped pages and changed hashes never suffice.
type instructionPage struct {
	index, sourceIndex, offset int
	page                       tools.SkillPage
}

func projectInstructionPages(messages []provider.ChatMessage) []instructionPage {
	type callSource struct {
		index int
		call  provider.ToolCall
	}
	var pages []instructionPage
	calls := make(map[string]callSource)
	for index, message := range messages {
		if message.Role == "system" && strings.HasPrefix(message.Content, inheritedRulesPrefix) {
			var rules []workerproto.ProjectRule
			if json.Unmarshal([]byte(strings.TrimPrefix(message.Content, inheritedRulesPrefix)), &rules) == nil {
				for _, rule := range rules {
					if filepath.IsAbs(rule.Path) && fmtHash(rule.Content) == rule.SHA256 {
						pages = append(pages, instructionPage{index: index, sourceIndex: index, page: tools.SkillPage{Path: rule.Path, SHA256: rule.SHA256, Content: rule.Content, NextOffset: len(rule.Content)}})
					}
				}
			}
		}
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				if call.Function.Name == "read_project_instruction" || scopedInstructionTool(call.Function.Name) {
					calls[call.ID] = callSource{index: index, call: call}
				}
			}
		}
		callID, content := message.ToolCallID, message.Content
		if message.RuntimeData {
			_, encoded, _ := strings.Cut(content, "\n")
			var event workerproto.RuntimeEvent
			if json.Unmarshal([]byte(encoded), &event) != nil || event.Kind != "tool" {
				continue
			}
			callID, content = event.SourceID, event.Content
		} else if message.Role != "tool" {
			continue
		}
		source, ok := calls[callID]
		if !ok {
			continue
		}
		call := source.call
		if call.Function.Name != "read_project_instruction" {
			var notice instructionNotice
			if json.Unmarshal([]byte(content), &notice) == nil && notice.Status == "instructions_required" && !notice.Executed {
				for _, page := range notice.Pages {
					for _, file := range notice.Instructions {
						if page.Path == file.Path && page.SHA256 == file.SHA256 && page.Offset >= 0 &&
							page.NextOffset-page.Offset == len(page.Content) && int64(page.NextOffset) <= file.Bytes &&
							(page.Truncated || int64(page.NextOffset) == file.Bytes) {
							pages = append(pages, instructionPage{index: index, sourceIndex: source.index, offset: page.Offset, page: page.SkillPage})
							break
						}
					}
				}
			}
			continue
		}
		var input struct {
			Path   string
			Offset int
		}
		var page tools.SkillPage
		if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || json.Unmarshal([]byte(content), &page) != nil ||
			page.SHA256 == "" || page.Path != filepath.Clean(input.Path) || input.Offset < 0 || page.NextOffset-input.Offset != len(page.Content) {
			continue
		}
		pages = append(pages, instructionPage{index: index, sourceIndex: source.index, offset: input.Offset, page: page})
	}
	return pages
}

func (s *projectInstructionState) acknowledge(messages []provider.ChatMessage) {
	s.read = nil
	s.pages = projectInstructionPages(messages)
	for _, item := range s.pages {
		page := item.page
		if s.read == nil {
			s.read = make(map[string]instructionRead)
		}
		read := s.read[page.Path]
		if read.hash != page.SHA256 {
			read = instructionRead{hash: page.SHA256}
		}
		read.ranges = append(read.ranges, instructionRange{item.offset, page.NextOffset})
		sort.Slice(read.ranges, func(i, j int) bool { return read.ranges[i].start < read.ranges[j].start })
		merged := read.ranges[:0]
		for _, span := range read.ranges {
			if len(merged) > 0 && span.start <= merged[len(merged)-1].end {
				merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
			} else {
				merged = append(merged, span)
			}
		}
		read.ranges = merged
		if !page.Truncated {
			read.total, read.hasEnd = page.NextOffset, true
		}
		s.read[page.Path] = read
	}
	// Ordinary scoped tools can also return JSON resembling a rule notice.
	// Only complete text matching the recorded hash can authorize an operation.
	for path, read := range s.read {
		if read.complete() {
			if _, valid := s.ruleText(path, read); !valid {
				delete(s.read, path)
			}
		}
	}
}

func fmtHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum)
}

func (s *projectInstructionState) ruleText(path string, read instructionRead) (string, bool) {
	if !read.complete() {
		return "", false
	}
	text := make([]byte, read.total)
	for _, item := range s.pages {
		if item.page.Path == path && item.page.SHA256 == read.hash {
			copy(text[item.offset:], item.page.Content)
		}
	}
	content := string(text)
	return content, fmtHash(content) == read.hash
}

func (s *projectInstructionState) inherited() []workerproto.ProjectRule {
	var rules []workerproto.ProjectRule
	for path, read := range s.read {
		if text, valid := s.ruleText(path, read); valid {
			rules = append(rules, workerproto.ProjectRule{Path: path, SHA256: read.hash, Content: text})
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Path < rules[j].Path })
	return rules
}

// Preserve actual, fully read, still-current rule pages through compaction.
// Keeping their complete assistant/tool groups also preserves native replay
// state and scope; no synthetic "already read" authorization is introduced.
func retainedProjectInstructionResults(ctx context.Context, messages []provider.ChatMessage) (map[int]bool, error) {
	state := projectInstructionState{}
	state.acknowledge(messages)
	current := make(map[string]string)
	for path, read := range state.read {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !read.complete() {
			continue
		}
		page, err := tools.ReadProjectInstruction(path, 0)
		if err == nil && page.SHA256 == read.hash {
			current[path] = read.hash
		}
	}
	keep := make(map[int]bool)
	for _, item := range state.pages {
		if hash, ok := current[item.page.Path]; ok && hash == item.page.SHA256 {
			keep[item.index] = true
			// Async rule text needs its originating assistant/tool pair too.
			keep[item.sourceIndex] = true
		}
	}
	return keep, ctx.Err()
}
