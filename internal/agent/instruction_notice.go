package agent

import (
	"encoding/json"

	"vista/internal/tools"
)

func scopedInstructionTool(name string) bool {
	switch name {
	case "apply_patch", "read_file", "write_file", "search", "find_files", "shell", "start_background", "read_resource", "read_skill":
		return true
	}
	return false
}

func hasInstructionPages(name, output string) bool {
	if name != "read_project_instruction" && !scopedInstructionTool(name) {
		return false
	}
	var fields struct {
		Status string          `json:"status"`
		Pages  json.RawMessage `json:"pages"`
		SHA256 string          `json:"sha256"`
	}
	if json.Unmarshal([]byte(output), &fields) != nil {
		return false
	}
	return name == "read_project_instruction" && fields.SHA256 != "" || fields.Status == "instructions_required" && len(fields.Pages) > 0
}

// Fit the entire notice, not just the text page, so the generic output budget
// never turns its rule text into an unacknowledgeable preview. Missing pages
// remain explicit and use the existing read_project_instruction continuation.
func (n *instructionNotice) addPage(page tools.SkillPage, offset int) bool {
	index := len(n.Pages)
	n.Pages = append(n.Pages, instructionTextPage{Offset: offset, SkillPage: page})
	setContent := func(size int) bool {
		prefix := textPrefix(page.Content, size)
		n.Pages[index].Content = prefix
		n.Pages[index].NextOffset = offset + len(prefix)
		n.Pages[index].Truncated = page.Truncated || len(prefix) < len(page.Content)
		encoded, _ := marshalResult(n)
		return !exceedsToolBudget(encoded)
	}
	low, high, best := 0, len(page.Content), -1
	for low <= high {
		probe := low + (high-low)/2
		if setContent(probe) {
			best = len(n.Pages[index].Content)
			low = probe + 1
		} else {
			high = probe - 1
		}
	}
	if best < 0 || best == 0 && len(page.Content) > 0 {
		n.Pages = n.Pages[:index]
		return false
	}
	setContent(best)
	return !n.Pages[index].Truncated
}
