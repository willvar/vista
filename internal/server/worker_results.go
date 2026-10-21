package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// workerResultConsumer owns the inbound protocol boundary: validate a frame,
// persist its effects, then notify readers or acknowledge committed work. It has
// no process ownership; the caller decides what to do when consumption fails.
type workerResultConsumer struct {
	done               <-chan struct{}
	refreshCredentials func(context.Context, modelcfg.Model) (modelcfg.Model, error)
	ctx                context.Context
	store              *store.Store
	sessionID          string
	stateDir           string
	send               func(workerproto.Command) error
	notify             func()
	pollInput          func(string, *workerproto.CompletedReply) (*workerproto.Command, error)
	metrics            func(workerproto.Metrics)
	finished           func()
}

func (a *App) workerResultConsumer(id string, w *worker) *workerResultConsumer {
	return &workerResultConsumer{
		ctx: a.ctx, store: a.store, sessionID: id, stateDir: a.dir,
		done: w.done, refreshCredentials: a.refreshModelCredentials,
		send:   w.send,
		notify: func() { a.hub.notify(id) },
		pollInput: func(runID string, reply *workerproto.CompletedReply) (*workerproto.Command, error) {
			return a.pullInterjection(a.ctx, id, runID, reply)
		},
		metrics:  func(metrics workerproto.Metrics) { a.recordWorkerMetrics(id, w, metrics) },
		finished: func() { go a.launchQueuedInput(id) },
	}
}

func (a *App) recordWorkerMetrics(id string, w *worker, metrics workerproto.Metrics) {
	var terminate bool
	a.mu.Lock()
	if a.workers[id] == w {
		w.metrics = metrics
		if !w.overBudget && overWorkerSoftLimit(metrics) {
			w.overBudget = true
			terminate = true
		}
	}
	a.mu.Unlock()
	if terminate {
		log.Printf("worker %s exceeded memory soft limit: heap=%d rss=%d", id, metrics.HeapBytes, metrics.RSSBytes)
		_ = w.cmd.Process.Kill()
	}
}

func (c *workerResultConsumer) consumeFrame(frame []byte) error {
	var result workerproto.Result
	if err := json.Unmarshal(frame, &result); err != nil {
		return fmt.Errorf("invalid result JSON: %w", err)
	}
	// Every descendant shares the run's configured depth. Transport records
	// accept all supported levels, including tool and task lifecycle events.
	if result.Depth < 0 || result.Depth > agentconfig.MaxDelegationDepth {
		return errors.New("invalid Agent depth")
	}
	return c.consume(result)
}

func (c *workerResultConsumer) consume(result workerproto.Result) error {
	switch result.Kind {
	case "memory_change":
		return c.memoryChange(result)
	case "model_credentials":
		return c.credentials(result)
	case "context_usage":
		if result.RunID == "" || result.Depth != 0 || result.TaskID != "" && result.TaskID != "main" || result.ContextUsage == nil || result.ContextUsage.Tokens < 0 {
			return errors.New("invalid context usage")
		}
		usage := *result.ContextUsage
		usage.RunID, usage.Sequence = result.RunID, 0
		return c.append(result, usage, "")
	case "context_status":
		return c.contextStatus(result)
	case "compaction_finished":
		if result.RunID == "" || (result.Status != "succeeded" && result.Status != "failed" && result.Status != "interrupted") || len(result.Content) > workerproto.MaxPromptBytes {
			return errors.New("invalid compaction result")
		}
		event, err := c.store.FinishCompaction(c.ctx, c.sessionID, result.RunID, result.Status, result.Content)
		return c.persisted(result, event, err, "")
	case "model_observation":
		return c.modelObservation(result)
	case "model_history":
		if result.History == nil || !validOutputPath(c.stateDir, result.History.Path) || result.AckID == "" || len(result.AckID) > 64 {
			return errors.New("invalid model history reference")
		}
		event, err := c.store.RecordModelHistory(c.ctx, c.sessionID, result)
		return c.persisted(result, event, err, result.AckID)
	case "input_poll":
		if result.RunID == "" || result.AckID == "" || len(result.AckID) > 64 || !validCompletedReply(result) {
			return errors.New("invalid input poll")
		}
		input, err := c.pollInput(result.RunID, result.Reply)
		ack := workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID, Input: input}
		if err != nil {
			ack.Error = "Could not deliver pending input: " + err.Error()
		}
		return c.send(ack)
	case "metrics":
		if result.Metrics == nil || len(result.Metrics.SampledAt) > 64 {
			return errors.New("invalid metrics")
		}
		c.metrics(*result.Metrics)
		return nil
	case "job":
		return c.job(result)
	case "artifact":
		return c.artifact(result)
	case "agent_task":
		if !validAgentTaskProgress(c.stateDir, result) {
			return errors.New("invalid agent task progress")
		}
		event, err := c.store.RecordAgentTask(c.ctx, c.sessionID, result)
		return c.persisted(result, event, err, result.AckID)
	case "tool":
		record, valid := prepareToolProgress(c.stateDir, result)
		if !valid {
			return errors.New("invalid tool progress")
		}
		ackID := ""
		if result.Status == "started" {
			ackID = result.AckID
		}
		return c.append(result, record, ackID)
	case "input_delivered":
		if result.RunID == "" || result.InputID == "" || len(result.InputID) > 64 || !validCompletedReply(result) {
			return errors.New("invalid input delivery")
		}
		message, err := c.store.DeliverInput(c.ctx, c.sessionID, result.RunID, result.InputID, uuid.NewString(), storeCompletedReply(result.Reply)...)
		if err == nil && message != nil {
			c.notify()
		}
		return err
	case "context_notice":
		return c.contextNotice(result)
	case "checklist":
		return c.checklist(result)
	case "model_request":
		if result.RunID == "" || result.ModelUse == nil || len(result.ModelUse.Provider) > 64 || len(result.ModelUse.ID) > 200 || len(result.ModelUse.Key) > 64 || len(result.ModelUse.ReasoningEffort) > 64 || (result.ModelUse.Purpose != "" && result.ModelUse.Purpose != "summary") {
			return errors.New("invalid model request record")
		}
		event, err := c.store.RecordModelRequestEvent(c.ctx, c.sessionID, result)
		return c.persisted(result, event, err, "")
	case "model_result":
		metadata := result.ModelResult
		if result.RunID == "" || result.Turn <= 0 || metadata == nil || metadata.Provider == "" || len(metadata.Provider) > 64 || metadata.ID == "" || len(metadata.ID) > 200 || len(metadata.FinishReason) > 64 || len(metadata.ReasoningEffort) > 64 || (metadata.Purpose != "" && metadata.Purpose != "summary") || result.Content != "" || result.Status != "" {
			return errors.New("invalid model result")
		}
		if usage := metadata.Usage; usage != nil && !validReportedUsage(*usage) {
			return errors.New("invalid model usage")
		}
		return c.append(result, result, "")
	case "source":
		parsed, err := url.Parse(result.Content)
		if result.RunID == "" || result.Turn <= 0 || result.Status != "" || len(result.Content) > 2048 || err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
			return errors.New("invalid model source")
		}
		return c.append(result, result, "")
	case "model_delta":
		if result.RunID == "" || result.Turn <= 0 || result.Content == "" || len(result.Content) > 2048 {
			return errors.New("invalid model text fragment")
		}
		event, err := c.store.AppendModelDelta(c.ctx, c.sessionID, result.RunID, result.Turn, result.Depth, result.Content, result.TaskID, result.ParentTaskID)
		return c.persisted(result, event, err, "")
	case "model_reasoning_delta":
		if result.RunID == "" || result.Turn <= 0 || result.Content == "" || len(result.Content) > 2048 || result.Status != "" {
			return errors.New("invalid model reasoning fragment")
		}
		return c.append(result, result, "")
	default:
		return c.finish(result)
	}
}

func validCompletedReply(result workerproto.Result) bool {
	reply := result.Reply
	return reply == nil || result.Depth == 0 && (result.TaskID == "" || result.TaskID == "main") && result.ParentTaskID == "" &&
		reply.Turn > 0 && reply.FullBytes > 0 && reply.FullBytes <= store.MaxModelOutputBytes &&
		strings.TrimSpace(reply.Content) != "" && len(reply.Content) <= workerproto.MaxPromptBytes && utf8.ValidString(reply.Content)
}

func storeCompletedReply(reply *workerproto.CompletedReply) []store.CompletedReply {
	if reply == nil {
		return nil
	}
	return []store.CompletedReply{{ID: uuid.NewString(), Content: reply.Content, Output: store.ModelOutputRef{Turn: reply.Turn, Bytes: reply.FullBytes}}}
}

func validReportedUsage(usage workerproto.ModelUsage) bool {
	const maxTokens = 1_000_000_000
	return usage.InputTokens >= 0 && usage.InputTokens <= maxTokens && usage.OutputTokens >= 0 && usage.OutputTokens <= maxTokens && usage.CacheReadTokens >= 0 && usage.CacheReadTokens <= maxTokens && usage.ReasoningTokens >= 0 && usage.ReasoningTokens <= maxTokens
}

func (c *workerResultConsumer) append(result workerproto.Result, data any, ackID string) error {
	event, err := c.store.AppendRunEvent(c.ctx, c.sessionID, result.RunID, result.Kind, data, result.TaskID)
	return c.persisted(result, event, err, ackID)
}

// ACK permits a Worker action to proceed. It must only be sent after the store
// confirms a committed event for an active run, never on a validation/store error.
func (c *workerResultConsumer) persisted(result workerproto.Result, event *store.Event, err error, ackID string) error {
	if err != nil {
		return fmt.Errorf("%s not persisted: %w", result.Kind, err)
	}
	if event == nil {
		if ackID != "" {
			return fmt.Errorf("%s belongs to an inactive run", result.Kind)
		}
		return nil
	}
	c.notify()
	if ackID != "" {
		if err := c.send(workerproto.Command{Action: "ack", RunID: result.RunID, AckID: ackID}); err != nil {
			return fmt.Errorf("%s acknowledgment failed: %w", result.Kind, err)
		}
	}
	return nil
}

func (c *workerResultConsumer) job(result workerproto.Result) error {
	job := result.Job
	if result.RunID == "" || job == nil || len(job.Command) > 4096 || len(job.Directory) > 4096 || len(job.Error) > 512 || job.Bytes < 0 || (result.Status != "starting" && result.Status != "running" && result.Status != "succeeded" && result.Status != "failed" && result.Status != "interrupted") || (result.Status == "starting" && (result.AckID == "" || len(result.AckID) > 64)) {
		return errors.New("invalid job progress")
	}
	if _, err := uuid.Parse(job.ID); err != nil {
		return errors.New("invalid job ID")
	}
	if !validOutputPath(c.stateDir, job.OutputPath) {
		return errors.New("invalid job output path")
	}
	if job.TaskID == "" {
		job.TaskID = result.TaskID
	}
	if job.ParentTaskID == "" {
		job.ParentTaskID = result.ParentTaskID
	}
	event, err := c.store.RecordJob(c.ctx, store.Job{StartedAt: job.StartedAt, ExpiresAt: job.ExpiresAt, Signal: job.Signal, TerminationReason: job.TerminationReason, CallID: job.CallID, TaskID: job.TaskID, ParentTaskID: job.ParentTaskID, ID: job.ID, SessionID: c.sessionID, RunID: result.RunID, Directory: job.Directory, Command: job.Command, OutputPath: job.OutputPath, Status: result.Status, Bytes: job.Bytes, ExitCode: job.ExitCode, Complete: job.Complete, Error: job.Error})
	ackID := ""
	if result.Status == "starting" {
		ackID = result.AckID
	}
	return c.persisted(result, event, err, ackID)
}

func (c *workerResultConsumer) artifact(result workerproto.Result) error {
	artifact := result.Artifact
	if result.RunID == "" || artifact == nil || artifact.Bytes < 0 || len(artifact.Error) > 512 || (result.Status != "started" && result.Status != "succeeded" && result.Status != "failed") || (result.Status == "started" && (result.AckID == "" || len(result.AckID) > 64)) {
		return errors.New("invalid artifact progress")
	}
	if _, err := uuid.Parse(artifact.ID); err != nil || !validOutputPath(c.stateDir, artifact.OutputPath) {
		return errors.New("invalid artifact identity or path")
	}
	if artifact.TaskID == "" {
		artifact.TaskID = result.TaskID
	}
	if artifact.ParentTaskID == "" {
		artifact.ParentTaskID = result.ParentTaskID
	}
	event, err := c.store.RecordArtifact(c.ctx, store.Artifact{CallID: artifact.CallID, TaskID: artifact.TaskID, ParentTaskID: artifact.ParentTaskID, ID: artifact.ID, SessionID: c.sessionID, RunID: result.RunID, OutputPath: artifact.OutputPath, Status: result.Status, Bytes: artifact.Bytes, Complete: artifact.Complete, Error: artifact.Error})
	ackID := ""
	if result.Status == "started" {
		ackID = result.AckID
	}
	return c.persisted(result, event, err, ackID)
}

func (c *workerResultConsumer) contextNotice(result workerproto.Result) error {
	if result.ContextUsage != nil && (result.Depth != 0 || result.ContextUsage.Tokens < 0) {
		return errors.New("invalid context usage")
	}
	if result.Context != nil && !validCompactionStatus(result.Context) {
		return errors.New("invalid compaction status")
	}
	if result.RunID == "" || result.Status != "succeeded" || result.Turn < 0 || result.Depth < 0 || result.Content == "" || len(result.Content) > 8<<10 || !utf8.ValidString(result.Content) || result.AckID == "" || len(result.AckID) > 64 {
		return errors.New("invalid context notice")
	}
	if result.Checkpoint != nil && (result.Depth != 0 || !validOutputPath(c.stateDir, result.Checkpoint.Path) || result.Checkpoint.ThroughSequence <= 0) {
		return errors.New("invalid context checkpoint")
	}
	return c.append(result, map[string]any{
		"contextUsage": result.ContextUsage,
		"checkpoint":   result.Checkpoint,
		"message":      "Completed execution history was compacted; original tool records remain available",
		"runId":        result.RunID, "taskId": result.TaskID, "parentTaskId": result.ParentTaskID, "summary": result.Content, "turn": result.Turn, "depth": result.Depth, "context": result.Context,
	}, result.AckID)
}

func (c *workerResultConsumer) checklist(result workerproto.Result) error {
	if result.RunID == "" || result.Checklist == nil || len(result.Checklist.Items) > 16 || result.AckID == "" || len(result.AckID) > 64 || result.Status != "succeeded" {
		return errors.New("invalid checklist")
	}
	items := make([]store.ChecklistItem, len(result.Checklist.Items))
	for index, item := range result.Checklist.Items {
		items[index] = store.ChecklistItem{Text: item.Text, Status: item.Status}
	}
	event, err := c.store.ReplaceChecklist(c.ctx, c.sessionID, result.RunID, items)
	return c.persisted(result, event, err, result.AckID)
}

func (c *workerResultConsumer) finish(result workerproto.Result) error {
	if result.Kind != "" || result.RunID == "" || (result.Status != "succeeded" && result.Status != "failed" && result.Status != "interrupted") || len(result.Content) > workerproto.MaxPromptBytes || (result.FullBytes != 0 && (result.Status != "succeeded" || result.Turn <= 0 || result.FullBytes > store.MaxModelOutputBytes)) || (result.FullBytes == 0 && result.Turn != 0) || (result.Pending != nil && !validPendingResult(result)) {
		return errors.New("invalid result fields")
	}
	var output []store.ModelOutputRef
	if result.FullBytes > 0 {
		output = []store.ModelOutputRef{{Turn: result.Turn, Bytes: result.FullBytes}}
	}
	var pendingReply *store.PendingReply
	if result.Pending != nil {
		pendingReply = &store.PendingReply{CallID: result.Pending.CallID, Kind: result.Pending.Kind, Content: result.Pending.Content, Questions: result.Pending.Questions}
	}
	event, err := c.store.FinishRunWithPending(c.ctx, c.sessionID, result.RunID, uuid.NewString(), result.Status, result.Content, pendingReply, output...)
	if err := c.persisted(result, event, err, ""); err != nil {
		return err
	}
	if event != nil {
		var finished struct {
			UnknownTools int `json:"unknownTools"`
		}
		_ = json.Unmarshal(event.Data, &finished)
		if result.Status == "succeeded" && finished.UnknownTools == 0 && result.Pending == nil {
			c.finished()
		}
	}
	return nil
}

func validPendingResult(result workerproto.Result) bool {
	pending := result.Pending
	if pending == nil || result.Status != "succeeded" || pending.Content == "" || pending.Content != result.Content {
		return false
	}
	item := &store.PendingReply{CallID: pending.CallID, Kind: pending.Kind, Content: pending.Content, Questions: pending.Questions}
	return store.ValidatePendingReply(item) == nil
}
