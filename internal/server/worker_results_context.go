package server

import (
	"errors"

	"vista/internal/workerproto"
)

func validCompactionStatus(s *workerproto.CompactionStatus) bool {
	return s != nil && len(s.Error) <= workerproto.MaxPromptBytes && (s.Phase == "compacting" || s.Phase == "compacted" || s.Phase == "failed")
}

func (c *workerResultConsumer) contextStatus(result workerproto.Result) error {
	if result.RunID == "" || result.Depth != 0 || !validCompactionStatus(result.Context) {
		return errors.New("invalid compaction status")
	}
	status := *result.Context
	status.RunID = result.RunID
	return c.append(result, status, "")
}
