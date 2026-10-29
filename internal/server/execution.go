package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
	"vista/internal/modelcatalog"
	"vista/internal/modeltrace"
	"vista/internal/platform"
	"vista/internal/provider"
	"vista/internal/question"
	"vista/internal/resources"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func supportsImageInput(protocol string) bool {
	return provider.SupportsImageInput(protocol)
}

func workerImages(images []store.Image) []workerproto.Image {
	result := make([]workerproto.Image, 0, len(images))
	for _, image := range images {
		result = append(result, image.Ref())
	}
	return result
}

func (a *App) submitMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	debug := requestDebugMode(r)
	var input struct {
		ReplacesMessageID string          `json:"replacesMessageId"`
		Content           string          `json:"content"`
		Mode              string          `json:"mode"`
		ModelKey          string          `json:"modelKey"`
		ReasoningEffort   string          `json:"reasoningEffort"`
		SelectionMode     string          `json:"selectionMode"`
		SelectionTier     string          `json:"selectionTier"`
		QuestionReply     *question.Reply `json:"questionReply,omitempty"`
		Images            []resources.Ref `json:"images"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid message", 400)
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.QuestionReply == nil && input.Content == "" && len(input.Images) == 0 || len(input.Content) > workerproto.MaxPromptBytes {
		http.Error(w, "message must have text or an image; text is limited to 8192 bytes", 400)
		return
	}
	images := make([]store.Image, 0, len(input.Images))
	for _, image := range input.Images {
		images = append(images, store.Image{Hash: image.Hash, MediaType: image.MediaType, Bytes: image.Bytes, Name: image.Name})
	}
	if len(images) > 0 {
		if err := a.admitHeavy(); err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
	}
	if err := store.ValidateImages(images); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	for _, image := range images {
		ref, err := a.store.SessionResource(r.Context(), sessionID, image.Hash)
		if err != nil || ref.MediaType != image.MediaType || ref.Bytes != image.Bytes {
			http.Error(w, "image resource is unavailable or metadata does not match this Session", 400)
			return
		}
	}

	if input.Mode == "" {
		input.Mode = "now"
	}
	if input.Mode != "now" && input.Mode != "later" {
		http.Error(w, "mode must be now or later", 400)
		return
	}
	item, err := a.store.Get(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "could not read input queue state", 500)
		return
	}
	if !item.Open {
		http.Error(w, store.ErrClosed.Error(), http.StatusConflict)
		return
	}
	if input.QuestionReply != nil {
		if input.ReplacesMessageID != "" {
			http.Error(w, "a question reply cannot replace a message", 400)
			return
		}
		if input.Content != "" || len(input.Images) != 0 || input.Mode != "now" {
			http.Error(w, "submit a question reply without additional content, images, or queue mode", http.StatusBadRequest)
			return
		}
		if item.Pending == nil || item.Pending.Kind != "question" || input.QuestionReply.RunID == "" || item.Pending.RunID != input.QuestionReply.RunID {
			http.Error(w, store.ErrNoPendingQuestion.Error(), http.StatusConflict)
			return
		}
		input.Content, err = question.FormatAnswers(item.Pending.Questions, input.QuestionReply.Answers)
		if err != nil || len(input.Content) > workerproto.MaxPromptBytes {
			message := "question reply exceeds 8192 bytes"
			if err != nil {
				message = err.Error()
			}
			http.Error(w, message, http.StatusBadRequest)
			return
		}
	}
	mode, tier := input.SelectionMode, input.SelectionTier
	if mode == "" {
		mode = item.SelectionMode
	}
	if tier == "" {
		tier = item.SelectionTier
	}
	if !validSelectionMode(mode) || !validSelectionTier(tier) {
		http.Error(w, "invalid selection mode or tier", 400)
		return
	}
	catalog := a.modelsSnapshot()
	if input.ReplacesMessageID != "" {
		// Reject malformed model/image choices before discarding usable history.
		if mode == "manual" {
			key := input.ModelKey
			if key == "" {
				key = item.ManualModelKey
			}
			if key == "" {
				key = item.ModelKey
			}
			model, resolved, resolveErr := catalog.Resolve(key)
			if resolveErr != nil {
				http.Error(w, resolveErr.Error(), 400)
				return
			}
			effort := input.ReasoningEffort
			if effort == "" {
				if resolved == item.ManualModelKey {
					effort = item.ManualReasoningEffort
				}
				effort = restoredManualEffort(model, effort)
			}
			if err := modelcatalog.ApplyReasoningEffort(&model, effort); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			imageAllowed := len(images) == 0
			for _, entry := range catalog.Models {
				if entry.Key == resolved && slices.Contains(entry.EffectiveInputModalities(), "image") && supportsImageInput(model.Provider) {
					imageAllowed = true
					break
				}
			}
			if !imageAllowed {
				http.Error(w, "selected model does not support image input", 400)
				return
			}
		}
		messageID, parseErr := uuid.Parse(input.ReplacesMessageID)
		if parseErr != nil {
			http.Error(w, "invalid replaced message", 400)
			return
		}
		if _, err := a.revertConversation(r.Context(), sessionID, messageID.String(), input.Images...); err != nil {
			revertHTTPError(w, err)
			return
		}
		item, err = a.store.Get(r.Context(), sessionID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	_, runningErr := a.store.RunningRunID(r.Context(), sessionID)
	if runningErr != nil && !errors.Is(runningErr, sql.ErrNoRows) {
		http.Error(w, "could not check active execution", 500)
		return
	}
	if input.QuestionReply != nil && runningErr == nil {
		http.Error(w, store.ErrNoPendingQuestion.Error(), http.StatusConflict)
		return
	}
	if !a.saveReplyLanguage(w, r, sessionID) {
		return
	}
	messageID := uuid.NewString()
	if input.QuestionReply == nil && runningErr == nil {
		// Queue acceptance does not select an automatic model or mutate an
		// active execution. Model/mode preferences apply when a new run starts.
		key, effort := input.ModelKey, input.ReasoningEffort
		if key == "" {
			key = item.ManualModelKey
		}
		if key == "" {
			key = item.ModelKey
		}
		if effort == "" && key == item.ManualModelKey {
			effort = item.ManualReasoningEffort
		}
		if mode == "auto" {
			key, effort = "", ""
		}
		if len(images) > 0 {
			allowed := false
			for _, entry := range catalog.Models {
				matches := entry.Key == key || mode == "auto" && input.Mode == "later"
				if input.Mode == "now" && runningErr == nil {
					matches = entry.Key == item.ModelKey
				}
				if matches && slices.Contains(entry.EffectiveInputModalities(), "image") && supportsImageInput(entry.Provider) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "selected model does not support image input", 400)
				return
			}
		}
		queued, event, queueErr := a.store.QueueInputWithDiagnostics(r.Context(), sessionID, messageID, input.Mode, input.Content, images, debug, key, effort, mode, tier)
		if queueErr == nil {
			a.notifySession(sessionID, "sessions")
			w.WriteHeader(202)
			jsonOut(w, map[string]any{"input": queued, "event": event})
			return
		}
		if !errors.Is(queueErr, store.ErrNotRunning) {
			inputHTTPError(w, queueErr)
			return
		}
		// The last decision boundary passed during acceptance. Start a new run.
	}
	a.mu.Lock()
	workerReady := workerCanReceive(a.workers[sessionID])
	a.mu.Unlock()
	if !workerReady {
		http.Error(w, "Worker is unavailable", 503)
		return
	}
	model, modelKey, err := a.resolveSelection(r.Context(), sessionID, selectionInput{
		SelectionMode: mode, SelectionTier: tier,
		ModelKey: input.ModelKey, ReasoningEffort: input.ReasoningEffort,
		InputTokens: store.SelectionTextTokens(input.Content), HasImages: len(images) > 0,
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	model.Key = modelKey
	dispatched := false
	defer func() {
		if !dispatched && mode == "auto" {
			a.finishUnobservedSelection(context.Background(), model, "unavailable")
		}
	}()
	if len(images) > 0 {
		allowed := false
		for _, entry := range catalog.Models {
			if entry.Key == modelKey && slices.Contains(entry.EffectiveInputModalities(), "image") {
				allowed = true
				break
			}
		}
		if !allowed || !supportsImageInput(model.Provider) {
			http.Error(w, "selected model does not support image input", 400)
			return
		}
	}
	runID := uuid.NewString()
	a.admissionMu.Lock()
	available, err := platform.AvailableMemory()
	if err != nil {
		a.admissionMu.Unlock()
		http.Error(w, "memory admission unavailable", 503)
		return
	}
	counts, err := a.store.SessionCounts(r.Context())
	if err != nil {
		a.admissionMu.Unlock()
		http.Error(w, "could not check execution capacity", 500)
		return
	}
	if err := a.admitRun(available, counts.Running); err != nil {
		a.admissionMu.Unlock()
		http.Error(w, err.Error(), 503)
		return
	}
	var message store.Message
	var event store.Event
	if input.QuestionReply != nil {
		message, event, err = a.store.BeginQuestionRun(r.Context(), sessionID, runID, messageID, *input.QuestionReply, modelKey)
	} else {
		message, event, err = a.store.BeginRunWithImages(r.Context(), sessionID, runID, messageID, input.Content, images, modelKey)
	}
	a.admissionMu.Unlock()
	if errors.Is(err, store.ErrNoPendingQuestion) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if errors.Is(err, store.ErrInvalidQuestionReply) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if errors.Is(err, store.ErrBusy) {
		http.Error(w, "session already has a running execution", 409)
		return
	}
	if errors.Is(err, store.ErrClosed) {
		http.Error(w, "session is closed", 409)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.notifySession(sessionID)
	a.background.Go(func() {
		a.generateSessionTitle(sessionID, message.ID, model, modeltrace.Scope{Enabled: debug, SessionID: sessionID, RunID: runID, Purpose: "title"})
	})
	if err := a.dispatchRun(r.Context(), sessionID, runID, message, model, debug); err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	dispatched = true
	w.WriteHeader(202)
	jsonOut(w, map[string]any{"runId": runID, "message": message, "event": event})
}

func (a *App) dispatchRun(ctx context.Context, sessionID, runID string, message store.Message, model modelcfg.Model, debug ...bool) error {
	model.SessionID = sessionID
	if err := a.store.RecordRunSelection(ctx, runID, model.ID, provider.ModelSelectionEffort(model), provider.ModelEndpointID(model)); err != nil {
		_, _ = a.store.FinishRun(context.Background(), sessionID, runID, "", "interrupted", "Could not record selected model")
		a.notifySession(sessionID)
		return err
	}
	language, err := a.store.ReplyLanguage(ctx, sessionID)
	if err != nil {
		_, _ = a.store.FinishRun(context.Background(), sessionID, runID, "", "interrupted", "Could not load reply language")
		a.notifySession(sessionID)
		return err
	}
	currentImages, err := a.store.MessageResources(ctx, sessionID, message.ID)
	if err != nil {
		_, _ = a.store.FinishRun(context.Background(), sessionID, runID, "", "interrupted", "Could not load image input")
		a.notifySession(sessionID)
		return errors.New("could not load image input")
	}
	command := workerproto.Command{SessionID: sessionID, MessageSequence: message.Sequence, RunID: runID, Prompt: message.Content, ReplyLanguage: language, QuestionCallID: message.QuestionCallID, Images: currentImages, Model: model, MCPServers: a.currentMCPServers(), SearchProviders: a.currentSearchProviders(), SearchPreviewBytes: a.currentSearchPreviewBytes(), Webfetch: a.currentWebfetchSettings()}
	command.Debug = len(debug) > 0 && debug[0]
	if err := a.dispatchWorker(ctx, sessionID, command); err != nil {
		completed, _ := a.store.FinishRun(context.Background(), sessionID, runID, "", "interrupted", "")
		if completed != nil {
			a.notifySession(sessionID)
		}
		return errors.New("Worker is unavailable; message was saved and execution interrupted")
	}
	return nil
}

func (a *App) sendWorker(id string, command workerproto.Command) error {
	a.mu.Lock()
	w := a.workers[id]
	if !workerCanReceive(w) {
		w = nil
	}
	a.mu.Unlock()
	if w == nil {
		return errors.New("worker unavailable")
	}
	return w.send(command)
}

// A run is visible in the database before context preparation finishes. A
// cancellation accepted during that interval must prevent its later dispatch.
func (a *App) dispatchWorker(ctx context.Context, id string, command workerproto.Command) error {
	if a.mcpHost != nil && command.Action != "compact" {
		command.MCPHost = a.mcpHost.Descriptor()
	}
	a.mu.Lock()
	w := a.workers[id]
	ready := workerCanReceive(w)
	a.mu.Unlock()
	if !ready {
		return errors.New("worker unavailable")
	}
	w.runMu.Lock()
	defer w.runMu.Unlock()
	runID, err := a.store.RunningRunID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && runID != command.RunID {
		return nil // Already canceled; keep the saved user message.
	}
	if err != nil {
		return err
	}
	if err := w.send(command); err != nil {
		return err
	}
	w.dispatchedRunID = command.RunID
	return nil
}

func (a *App) cancelRun(ctx context.Context, id, runID string) error {
	a.mu.Lock()
	w := a.workers[id]
	ready := workerCanReceive(w)
	a.mu.Unlock()
	if !ready {
		return errors.New("worker unavailable")
	}
	w.runMu.Lock()
	defer w.runMu.Unlock()
	if w.dispatchedRunID == runID {
		return w.send(workerproto.Command{Action: "cancel", RunID: runID})
	}
	event, err := a.store.FinishRun(ctx, id, runID, "", "interrupted", "Cancelled by user before execution started")
	if event != nil {
		a.notifySession(id)
	}
	return err
}

func (w *worker) send(command workerproto.Command) error {
	encoded, err := json.Marshal(command)
	if err != nil {
		return err
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_, err = w.input.Write(append(encoded, '\n'))
	return err
}
func (a *App) readWorkerOutput(id string, w *worker, scanner *bufio.Scanner) {
	consumer := a.workerResultConsumer(id, w)
	for scanner.Scan() {
		if err := consumer.consumeFrame(scanner.Bytes()); err != nil {
			log.Printf("worker %s result rejected: %v", id, err)
			break
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("worker %s output error: %v", id, err)
	}
	a.mu.Lock()
	w.exiting = true
	a.mu.Unlock()
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		if err := platform.KillSessionDescendants(w.cmd.Process.Pid); err != nil {
			log.Printf("worker %s descendant cleanup: %v", id, err)
		} else if err := a.store.ClearWorkerOwner(context.Background(), w.owner); err != nil {
			log.Printf("worker %s ownership record cleanup: %v", id, err)
		}
	}
	err := w.cmd.Wait()
	a.mu.Lock()
	stopping := w.stopping
	overBudget := w.overBudget
	restartAllowed := false
	if !stopping {
		restartAllowed = a.recordWorkerFailureLocked(id, time.Now())
	}
	a.mu.Unlock()
	if !stopping {
		reason := "Worker exited unexpectedly"
		if overBudget {
			reason = "Worker exceeded its memory soft limit"
		}
		events, e := a.store.InterruptRunning(a.ctx, id, reason)
		if e != nil {
			log.Printf("worker %s interrupt record failed: %v", id, e)
		}
		for range events {
			a.notifySession(id)
		}
		jobEvents, jobErr := a.store.InterruptJobs(a.ctx, id, reason)
		if jobErr != nil {
			log.Printf("worker %s job interrupt record failed: %v", id, jobErr)
		}
		for range jobEvents {
			a.notifySession(id)
		}
		artifactEvents, artifactErr := a.store.InterruptArtifacts(a.ctx, id, reason)
		if artifactErr != nil {
			log.Printf("worker %s artifact interrupt record failed: %v", id, artifactErr)
		}
		for range artifactEvents {
			a.notifySession(id)
		}
		log.Printf("worker %s exited: %v", id, err)
	}
	// Keep the old instance registered until all of its terminal effects are
	// persisted. A replacement must not have its runs interrupted by this reader.
	a.mu.Lock()
	if a.workers[id] == w {
		delete(a.workers, id)
	}
	close(w.done)
	a.mu.Unlock()
	if !stopping {
		a.updates.notify("sessions", "status")
		if restartAllowed {
			a.background.Go(func() { a.recoverWorker(id) })
		} else {
			log.Printf("worker %s automatic restart suppressed after repeated exits; retry manually", id)
		}
	}
}

func (a *App) launchQueuedInput(sessionID string) {
	unlock := a.lockSessionMutation(sessionID)
	defer unlock()
	a.admissionMu.Lock()
	a.mu.Lock()
	workerReady := workerCanReceive(a.workers[sessionID])
	a.mu.Unlock()
	if !workerReady {
		a.admissionMu.Unlock()
		return
	}
	if _, err := a.store.RunningRunID(a.ctx, sessionID); err == nil {
		a.admissionMu.Unlock()
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.admissionMu.Unlock()
		return
	}
	input, err := a.store.NextQueuedInput(a.ctx, sessionID)
	if err != nil || input == nil {
		a.admissionMu.Unlock()
		if err != nil {
			log.Printf("session %s queued input lookup: %v", sessionID, err)
		}
		return
	}
	available, err := platform.AvailableMemory()
	if err == nil {
		var counts store.Counts
		counts, err = a.store.SessionCounts(a.ctx)
		if err == nil {
			err = a.admitRun(available, counts.Running)
		}
	}
	if err != nil {
		if markErr := a.store.MarkQueuedInputs(a.ctx, sessionID, "", "capacity_rejected"); markErr != nil {
			log.Printf("session %s queued input rejection failed: %v", sessionID, markErr)
		}
		a.admissionMu.Unlock()
		a.notifySession(sessionID)
		return
	}
	selection, err := a.store.Get(a.ctx, sessionID)
	if err != nil {
		a.admissionMu.Unlock()
		return
	}
	// Pending items retain their submission metadata for display. The next
	// execution uses the user's current preference, including edits made while
	// the previous task was running.
	model, _, err := a.resolveSelection(a.ctx, sessionID, selectionInput{
		SelectionMode: selection.SelectionMode, SelectionTier: selection.SelectionTier,
		ModelKey: selection.ManualModelKey, ReasoningEffort: selection.ManualReasoningEffort,
		InputTokens: store.SelectionTextTokens(input.Content), HasImages: input.ImageCount > 0,
	})
	if err != nil {
		_ = a.store.MarkQueuedInputs(a.ctx, sessionID, "", "interrupted")
		a.admissionMu.Unlock()
		a.notifySession(sessionID)
		log.Printf("session %s queued model unavailable: %v", sessionID, err)
		return
	}
	dispatched := false
	defer func() {
		if !dispatched && selection.SelectionMode == "auto" {
			a.finishUnobservedSelection(context.Background(), model, "unavailable")
		}
	}()
	runID := uuid.NewString()
	message, err := a.store.BeginQueuedRun(a.ctx, sessionID, input.ID, runID, uuid.NewString(), model.Key)
	a.admissionMu.Unlock()
	if errors.Is(err, store.ErrBusy) || errors.Is(err, store.ErrClosed) {
		return
	}
	if err != nil {
		log.Printf("session %s queued input start: %v", sessionID, err)
		return
	}
	a.notifySession(sessionID)
	a.background.Go(func() {
		a.generateSessionTitle(sessionID, message.ID, model, modeltrace.Scope{Enabled: input.Debug, SessionID: sessionID, RunID: runID, Purpose: "title"})
	})
	if err := a.dispatchRun(a.ctx, sessionID, runID, *message, model, input.Debug); err != nil {
		log.Printf("session %s queued input dispatch: %v", sessionID, err)
		return
	}
	dispatched = true
}
