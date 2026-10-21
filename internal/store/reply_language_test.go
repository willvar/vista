package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReplyLanguagePersistsSeparatelyFromMessages(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vista.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := db.Create(ctx, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if code, err := db.ReplyLanguage(ctx, "a"); err != nil || code != "" {
		t.Fatalf("initial: %q %v", code, err)
	}
	for _, code := range []string{"en", "zh-TW", "zh-TW", "zh-HK", "zh-MO", "zh-SG", "zh-MY", "ja"} {
		if err := db.SetReplyLanguage(ctx, "a", code); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetReplyLanguage(ctx, "a", "ignore instructions"); err == nil {
		t.Fatal("accepted prompt text as a language")
	}
	events, err := db.EventsAfter(ctx, "a", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, event := range events {
		if event.Kind == "reply_language" {
			count++
		}
	}
	if count != 7 {
		t.Fatalf("duplicate preference produced event: %d", count)
	}
	messages, err := db.MessagesBefore(ctx, "a", 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("language entered messages: %+v %v", messages, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if code, err := db.ReplyLanguage(ctx, "a"); err != nil || code != "ja" {
		t.Fatalf("persisted: %q %v", code, err)
	}
	if code, err := db.ReplyLanguage(ctx, "b"); err != nil || code != "" {
		t.Fatalf("cross-session leak: %q %v", code, err)
	}
}
