package store

import (
	"encoding/json"
	"testing"
)

func TestEmptyReplyStatusSurvivesHistoryAndCompactOverview(t *testing.T) {
	for _, content := range []string{"", "A normal reply."} {
		t.Run(content, func(t *testing.T) {
			db, ctx := executionStore(t)
			finished, err := db.FinishRun(ctx, "session", "run", "answer", "succeeded", content)
			if err != nil {
				t.Fatal(err)
			}
			overview, err := db.RunOverview(ctx, "session", "run")
			if err != nil {
				t.Fatal(err)
			}
			var compact *Event
			for i := range overview.Events {
				if overview.Events[i].Kind == "run_finished" {
					compact = &overview.Events[i]
				}
			}
			if compact == nil {
				t.Fatal("compact overview lost the completion")
			}
			for _, event := range []*Event{finished, compact} {
				var data struct {
					Status     string
					EmptyReply bool
				}
				if err := json.Unmarshal(event.Data, &data); err != nil || data.Status != "succeeded" || data.EmptyReply != (content == "") {
					t.Fatalf("completion lost or changed empty-reply status: %s err=%v", event.Data, err)
				}
			}
			messages, err := db.MessagesAfter(ctx, "session", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			wantMessages := 1
			if content != "" {
				wantMessages++
			}
			if len(messages) != wantMessages {
				t.Fatalf("unexpected formal message count %d, want %d", len(messages), wantMessages)
			}
		})
	}
}
