package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/sessiontitle"
	"vista/internal/workerproto"
)

var titleSlots = make(chan struct{}, 2)

// Some models always reason before returning text, even for a short title.
// Allow the same wait as a normal model request instead of cutting them off
// before their first text token.
const sessionTitleTimeout = 2 * time.Minute

var errSessionTitleTools = sessiontitle.ErrTools
var errSessionTitleEmpty = sessiontitle.ErrEmpty

func (a *App) generateSessionTitle(sessionID, messageID string, model modelcfg.Model, diagnostics ...modeltrace.Scope) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, sessionTitleTimeout)
	defer cancel()
	if len(diagnostics) > 0 {
		ctx = modeltrace.WithScope(ctx, diagnostics[0], a.updates.diagnosticSaved)
		if diagnostics[0].Enabled {
			defer func() {
				// A title request may finish after the conversation was deleted.
				if _, err := a.store.Get(context.Background(), sessionID); errors.Is(err, sql.ErrNoRows) {
					_ = modeltrace.DeleteSession(a.dir, sessionID)
				}
			}()
		}
	}
	prompt, eligible, err := a.store.FirstMessageTitleCandidate(ctx, sessionID, messageID)
	if err != nil {
		logSessionTitleFailure(sessionID, model, "candidate", err)
		return
	}
	if !eligible {
		return
	}
	settings, err := agentconfig.ReadSettings(a.configDir)
	if err != nil {
		logSessionTitleFailure(sessionID, model, "settings", err)
		return
	}
	if !settings.AutomaticTitles {
		return
	}
	select {
	case titleSlots <- struct{}{}:
		defer func() { <-titleSlots }()
	case <-ctx.Done():
		logSessionTitleFailure(sessionID, model, "queue", ctx.Err())
		return
	}
	language, err := a.store.ReplyLanguage(ctx, sessionID)
	if err != nil {
		logSessionTitleFailure(sessionID, model, "language", err)
		return
	}
	ctx = replylanguage.WithDefault(ctx, language)
	refs, err := a.store.MessageResources(ctx, sessionID, messageID)
	if err != nil {
		logSessionTitleFailure(sessionID, model, "images", err)
		return
	}
	var title string
	if len(refs) == 0 {
		title, err = makeSessionTitle(provider.WithCredentialResolver(ctx, a.refreshModelCredentials), model, prompt, nil)
	} else {
		if err = a.admitHeavy(); err != nil {
			logSessionTitleFailure(sessionID, model, "capacity", err)
			return
		}
		a.mu.Lock()
		worker := a.workers[sessionID]
		ready := workerCanReceive(worker)
		a.mu.Unlock()
		if !ready {
			return
		}
		var result workerproto.Result
		command := workerproto.Command{Action: "title", SessionID: sessionID, Prompt: prompt, Images: refs, Model: model, ReplyLanguage: language}
		if len(diagnostics) > 0 {
			command.RunID, command.Debug = diagnostics[0].RunID, diagnostics[0].Enabled
		}
		result, err = worker.rpc(ctx, command)
		title = result.Content
	}
	if err != nil {
		logSessionTitleFailure(sessionID, model, "model", err)
		return
	}
	a.saveSessionTitle(ctx, sessionID, messageID, title, model)
}

func (a *App) saveSessionTitle(ctx context.Context, sessionID, messageID, title string, model modelcfg.Model) {
	event, err := a.store.SetGeneratedTitle(ctx, sessionID, messageID, title)
	if err != nil {
		logSessionTitleFailure(sessionID, model, "save", err)
		return
	}
	if event != nil {
		a.notifySession(sessionID, "sessions")
	}
}

func logSessionTitleFailure(sessionID string, model modelcfg.Model, stage string, err error) {
	reason := "failed"
	status := 0
	var upstream *provider.ModelHTTPError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	case errors.Is(err, context.Canceled):
		reason = "cancelled"
	case errors.Is(err, errSessionTitleTools):
		reason = "unexpected_tools"
	case errors.Is(err, errSessionTitleEmpty):
		reason = "invalid_title"
	case errors.As(err, &upstream):
		reason, status = "upstream_http", upstream.StatusCode
	}
	// Error messages may contain request URLs, credentials, or echoed prompts.
	// Log only local identifiers and a bounded classification of the failure.
	log.Printf("session title generation failed: session=%q provider=%q model=%q stage=%s reason=%s status=%d", sessionID, model.ProviderID, model.ID, stage, reason, status)
}

func makeSessionTitle(ctx context.Context, model modelcfg.Model, prompt string, images []provider.Image) (string, error) {
	return sessiontitle.Generate(ctx, model, prompt, images)
}
func titleExcerpt(value string, limit int) string { return sessiontitle.Excerpt(value, limit) }
func cleanSessionTitle(value string) string       { return sessiontitle.Clean(value) }
