package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
)

func fixtureImage(t *testing.T) Image {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return Image{MediaType: "image/png", Data: data}
}

func TestImagesPersistAcrossRunsWithoutEnteringEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const session = "00000000-0000-4000-8000-000000000092"
	if _, err := s.Create(ctx, session, "images"); err != nil {
		t.Fatal(err)
	}
	image := fixtureImage(t)
	message, event, err := s.BeginRunWithImages(ctx, session, "run-1", "message-1", "look", []Image{image, image, image})
	if err != nil || message.ImageCount != 3 || message.ImageBytes != 3*len(image.Data) || bytes.Contains(event.Data, image.Data) || len(event.Data) > 1024 {
		t.Fatalf("image metadata/event: %+v %+v %v", message, event, err)
	}
	queued, queuedEvent, err := s.QueueInputWithImages(ctx, session, "queued-now", "now", "and this", []Image{image, image, image})
	if err != nil || queued.ImageCount != 3 || len(queuedEvent.Data) > 1024 {
		t.Fatalf("queued image metadata/event: %+v %+v %v", queued, queuedEvent, err)
	}
	delivered, err := s.DeliverInput(ctx, session, "run-1", queued.ID, "message-2")
	if err != nil || delivered == nil || delivered.ImageCount != 3 || delivered.ImageBytes != 3*len(image.Data) {
		t.Fatalf("image interjection: %+v %v", delivered, err)
	}
	var duplicateCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM queued_input_resources WHERE input_id=?", queued.ID).Scan(&duplicateCount); err != nil || duplicateCount != 0 {
		t.Fatalf("delivered image kept duplicate blob: %d %v", duplicateCount, err)
	}
	if _, err := s.FinishRun(ctx, session, "run-1", "answer-1", "succeeded", "seen"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginRun(ctx, session, "run-2", "message-3", "next"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueInputWithImages(ctx, session, "queued-later", "later", "", []Image{image, image, image}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, session, "run-2", "answer-2", "succeeded", "ready"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pending, err := s.NextQueuedInput(ctx, session)
	if err != nil || pending == nil || pending.ID != "queued-later" || pending.ImageCount != 3 {
		t.Fatalf("queued image after restart: %+v %v", pending, err)
	}
	later, err := s.BeginQueuedRun(ctx, session, pending.ID, "run-3", "message-4")
	if err != nil || later == nil || later.ImageCount != 3 {
		t.Fatalf("deferred image input: %+v %v", later, err)
	}
	for _, id := range []string{message.ID, delivered.ID, later.ID} {
		images, err := s.MessageImages(ctx, session, id)
		if err != nil || len(images) != 3 || images[0].MediaType != image.MediaType || !bytes.Equal(images[0].Data, image.Data) {
			t.Fatalf("image %s was not durable: %+v %v", id, images, err)
		}
	}
	page, err := s.MessagesBefore(ctx, session, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page {
		if item.ID == later.ID && item.ImageCount != 3 {
			t.Fatalf("message list lost image count: %+v", item)
		}
		encoded, _ := json.Marshal(item)
		if bytes.Contains(encoded, image.Data) || bytes.Contains(encoded, []byte("data:image")) {
			t.Fatalf("message list exposed image bytes: %s", encoded)
		}
	}
	if err := s.Delete(ctx, session); err != nil {
		t.Fatal(err)
	}
	if images, err := s.MessageImages(ctx, session, message.ID); err != nil || len(images) != 0 {
		t.Fatalf("deleted Session kept images: %+v %v", images, err)
	}
}
