package worker

import (
	"context"
	"path/filepath"

	"vista/internal/instance"
	"vista/internal/modeltrace"
	"vista/internal/platform"
	"vista/internal/provider"
	"vista/internal/replylanguage"
	"vista/internal/sessiontitle"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func resourceTitle(ctx context.Context, command workerproto.Command, admit func(context.Context, workerproto.Result) error, emit func(workerproto.Result) error) (string, error) {
	if err := admit(ctx, workerproto.Result{Kind: "media_admission", RunID: command.RunID, Metrics: &workerproto.Metrics{RSSBytes: platform.RSS()}}); err != nil {
		return "", err
	}
	ctx = modeltrace.WithScope(ctx, modeltrace.Scope{Enabled: command.Debug, SessionID: command.SessionID, RunID: command.RunID, Purpose: "title"}, func(summary modeltrace.Summary) {
		_ = emit(workerproto.Result{Kind: "model_diagnostic", RunID: summary.RunID, DiagnosticID: summary.ID})
	})
	dir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	db, err := store.OpenReadOnly(filepath.Join(dir, "vista.db"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	// Resolve only references owned by this conversation.
	for _, ref := range command.Images {
		if _, err = db.SessionResource(ctx, command.SessionID, ref.Hash); err != nil {
			return "", err
		}
	}
	images, err := db.ResolveImages(ctx, command.Images)
	if err != nil {
		return "", err
	}
	visual := make([]provider.Image, 0, len(images))
	for _, image := range images {
		visual = append(visual, provider.Image{MediaType: image.MediaType, Data: image.Data})
	}
	return sessiontitle.Generate(replylanguage.WithDefault(ctx, command.ReplyLanguage), command.Model, command.Prompt, visual)
}
