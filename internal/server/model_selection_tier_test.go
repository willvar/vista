package server

import (
	"context"
	"testing"
	"vista/internal/store"
)

func TestManualSelectionDoesNotGuessAmongMultipleModels(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	extra := a.models.Models[0]
	extra.Key = "different-connection"
	a.models.Models = append(a.models.Models, extra)
	if _, err := a.store.Create(ctx, "empty", "title"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "empty", selectionInput{}); err == nil {
		t.Fatal("silently picked a model")
	}
	if _, key, err := a.resolveSelection(ctx, "empty", selectionInput{ModelKey: extra.Key}); err != nil || key != extra.Key {
		t.Fatalf("explicit model: %q %v", key, err)
	}
	if _, key, err := a.resolveSelection(ctx, "empty", selectionInput{}); err != nil || key != extra.Key {
		t.Fatalf("did not retain selection: %q %v", key, err)
	}
}

func TestManualSelectionDoesNotReplaceRetiredConnection(t *testing.T) {
	a := selectionApp(t)
	ctx := context.Background()
	key := a.models.Models[0].Key
	fallback := a.models.Models[0]
	fallback.Key = "other"
	a.models.Models[0] = a.models.Models[0].WithStatus("retired")
	a.models.Models = append(a.models.Models, fallback)
	if _, err := a.store.Create(ctx, "old", "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.PatchSessionSelection(ctx, "old", store.SessionSelectionPatch{ManualModelKey: &key}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.resolveSelection(ctx, "old", selectionInput{}); err == nil {
		t.Fatal("retired selection ran or changed to another connection")
	}
	item, err := a.store.Get(ctx, "old")
	if err != nil || item.ManualModelKey != key {
		t.Fatalf("saved choice was overwritten: %+v %v", item, err)
	}
}
