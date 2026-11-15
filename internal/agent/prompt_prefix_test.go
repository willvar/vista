package agent

import "strings"

func runtimeNoticeOnWire(role, content string) bool {
	return role == "system" && strings.HasPrefix(content, "Runtime completion event.")
}
