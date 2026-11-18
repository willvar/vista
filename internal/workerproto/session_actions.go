package workerproto

import (
	"errors"

	"github.com/google/uuid"
)

const MaxSessionActionTargets = 20

// The Worker supplies a kind and exact IDs, never approval or target revisions.
type SessionAction struct {
	Kind       string   `json:"kind"`
	SessionIDs []string `json:"sessionIds"`
}

func (a SessionAction) Validate(source string) error {
	if a.Kind != "close" && a.Kind != "delete" || source == "" || len(a.SessionIDs) == 0 || len(a.SessionIDs) > MaxSessionActionTargets {
		return errors.New("provide 1 to 20 explicit session IDs")
	}
	seen := map[string]bool{}
	for _, id := range a.SessionIDs {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id || id == source || seen[id] {
			return errors.New("use distinct canonical session IDs other than the current session")
		}
		seen[id] = true
	}
	return nil
}

type SessionActionResult struct {
	Kind  string              `json:"kind"`
	Items []SessionActionItem `json:"items"`
}

type SessionActionItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}
