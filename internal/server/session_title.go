package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/modeltrace"
	"vista/internal/provider"
	"vista/internal/replylanguage"
)

var titleSlots = make(chan struct{}, 2)

// Some models always reason before returning text, even for a short title.
// Allow the same wait as a normal model request instead of cutting them off
// before their first text token.
const sessionTitleTimeout = 2 * time.Minute

var errSessionTitleTools = errors.New("title response requested tools")
var errSessionTitleEmpty = errors.New("title response contained no usable title")

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
	storedImages, err := a.store.MessageImages(ctx, sessionID, messageID)
	if err != nil {
		logSessionTitleFailure(sessionID, model, "images", err)
		return
	}
	images := make([]provider.Image, 0, len(storedImages))
	for _, image := range storedImages {
		images = append(images, provider.Image{MediaType: image.MediaType, Data: image.Data})
	}
	title, err := makeSessionTitle(provider.WithCredentialResolver(ctx, a.refreshModelCredentials), model, prompt, images)
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
	model = provider.ForPurpose(model, provider.PurposeTitle)
	if model.ProviderID == "opencode-go" {
		// Zen Go requires a routing session header even for one-shot requests.
		// Keep title generation out of the foreground conversation by using an
		// ephemeral side-request identity.
		model.SessionID = uuid.NewString()
	}
	system := "Write a short, specific title for the user's message. Return only the title, without quotes, markdown, or a trailing period. Use at most 8 words or 24 Chinese characters. Name the task described in the message and images without answering or carrying it out."
	messages := []provider.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: "User message:\n" + titleExcerpt(prompt, 1200), Images: images},
	}
	// Title generation does not expose reasoning to the UI, but providers may
	// still stream it before the title. Consume and discard those deltas so a
	// reasoning-capable model can complete the request.
	turn, err := provider.ModelTurnStreamWithReasoning(ctx, model, messages, nil, nil, func(string) error { return nil })
	if err != nil {
		return "", err
	}
	if len(turn.Calls) != 0 || len(turn.HostedCalls) != 0 {
		return "", errSessionTitleTools
	}
	title := cleanSessionTitle(turn.Text)
	if title == "" {
		return "", errSessionTitleEmpty
	}
	return title, nil
}

func titleExcerpt(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func cleanSessionTitle(value string) string {
	value = strings.TrimSpace(strings.SplitN(value, "\n", 2)[0])
	value = strings.Trim(value, " \t\r\"'`“”‘’《》「」『』#*。.!！?？:：")
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || value == "New session" || utf8.RuneCountInString(value) > 60 || len(value) > 200 {
		return ""
	}
	return value
}
