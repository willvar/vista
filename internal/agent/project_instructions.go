package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"

	"vista/internal/provider"
	"vista/internal/tools"
)

type instructionRange struct{ start, end int }

type instructionRead struct {
	hash   string
	ranges []instructionRange
	total  int
	hasEnd bool
}

// Each task owns its model's read coverage. The run loop rebuilds it from the
// context consumed by a successful model request, before starting its tools.
// Tool goroutines only inspect it, so a concurrent read cannot authorize a
// write from the same model turn. Compacted-away pages and other tasks' reads
// cannot authorize an operation either.
type projectInstructionState struct {
	global tools.InstructionFile // The exact version loaded into the system prompt.
	read   map[string]instructionRead
}

type instructionNotice struct {
	Status       string                  `json:"status"`
	Executed     bool                    `json:"executed"`
	Target       string                  `json:"target"`
	Instructions []tools.InstructionFile `json:"instructions"`
	Message      string                  `json:"message"`
}

func (s *projectInstructionState) prepare(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct{ Path, Root, Directory string }
	switch call.Function.Name {
	case "read_file", "write_file", "search", "shell", "start_background", "read_resource", "read_skill":
		if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
			return "", err
		}
	default:
		return "", nil
	}
	target := input.Path
	switch call.Function.Name {
	case "search":
		target = input.Root
	case "shell", "start_background":
		target = input.Directory
	}
	// Hash-based resources and embedded skill:// documents have no filesystem
	// target. Invalid/missing paths remain the individual handler's concern.
	if !filepath.IsAbs(target) {
		return "", nil
	}
	target = filepath.Clean(target)
	files, err := tools.DiscoverProjectInstructions(ctx, target)
	if err != nil {
		return "", err
	}
	var unread []tools.InstructionFile
	for _, file := range files {
		if file.Path == s.global.Path && file.SHA256 == s.global.SHA256 {
			continue
		}
		read := s.read[file.Path]
		if read.hash != file.SHA256 || !read.hasEnd || len(read.ranges) != 1 || read.ranges[0].start != 0 || read.ranges[0].end != read.total {
			unread = append(unread, file)
		}
	}
	if len(unread) == 0 {
		return "", nil
	}
	return marshalResult(instructionNotice{
		Status: "instructions_required", Target: target, Instructions: unread,
		Message: "Operation not executed. Read all pages of these rules with read_project_instruction, apply them within their directory scope, then retry or revise this operation.",
	})
}

// Only complete tool pages actually present in the request acknowledge text.
// Discovery metadata, previews, skipped pages and changed hashes never suffice.
func (s *projectInstructionState) acknowledge(messages []provider.ChatMessage) {
	s.read = nil
	calls := make(map[string]provider.ToolCall)
	for _, message := range messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				if call.Function.Name == "read_project_instruction" {
					calls[call.ID] = call
				}
			}
		}
		if message.Role != "tool" {
			continue
		}
		call, ok := calls[message.ToolCallID]
		delete(calls, message.ToolCallID)
		if !ok {
			continue
		}
		var input struct {
			Path   string
			Offset int
		}
		var page tools.SkillPage
		if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || json.Unmarshal([]byte(message.Content), &page) != nil ||
			page.SHA256 == "" || page.Path != filepath.Clean(input.Path) || input.Offset < 0 || page.NextOffset-input.Offset != len(page.Content) {
			continue
		}
		if s.read == nil {
			s.read = make(map[string]instructionRead)
		}
		read := s.read[page.Path]
		if read.hash != page.SHA256 {
			read = instructionRead{hash: page.SHA256}
		}
		read.ranges = append(read.ranges, instructionRange{input.Offset, page.NextOffset})
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
}
