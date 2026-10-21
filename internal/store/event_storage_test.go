package store

import (
	"reflect"
	"testing"

	"vista/internal/workerproto"
)

func TestEventReadersAcceptSQLiteJSONTextWithoutLosingData(t *testing.T) {
	st, ctx := executionStore(t)
	var turnEvents []Event
	for _, result := range []workerproto.Result{
		{RunID: "run", Kind: "model_result", Turn: 1, ModelResult: &workerproto.ModelResult{Provider: "openai-chat", ID: "test", FinishReason: "stop"}},
		{RunID: "run", Kind: "tool", Turn: 1, Status: "succeeded", Content: "完整输出 café 🙂", Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell"}},
	} {
		event, err := st.AppendRunEvent(ctx, "session", "run", result.Kind, result)
		if err != nil {
			t.Fatal(err)
		}
		turnEvents = append(turnEvents, *event)
	}
	artifact := Artifact{ID: "artifact", SessionID: "session", RunID: "run", CallID: "call", TaskID: "main", OutputPath: "output.log"}
	for _, status := range []string{"started", "succeeded"} {
		artifact.Status = status
		if _, err := st.RecordArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.FinishRun(ctx, "session", "run", "reply", "succeeded", "answer"); err != nil {
		t.Fatal(err)
	}
	originals, err := st.EventsAfter(ctx, "session", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// SQLite JSON edits return TEXT even when the original column holds a BLOB.
	// Keep the first event as BLOB to exercise a page containing both types.
	if _, err := st.db.ExecContext(ctx, `UPDATE events SET data=json_remove(data,'$.unused') WHERE sequence>1`); err != nil {
		t.Fatal(err)
	}
	for _, read := range []struct {
		name string
		call func() ([]Event, error)
		want []Event
	}{
		{"forward page", func() ([]Event, error) { return st.EventsAfter(ctx, "session", 0, 100) }, originals},
		{"backward page", func() ([]Event, error) { return st.EventsBefore(ctx, "session", 0, 100) }, originals},
		{"model results", func() ([]Event, error) { return st.ModelResultEvents(ctx, "session", []string{"run"}) }, turnEvents[:1]},
		{"model turn", func() ([]Event, error) { return st.RunTurnEvents(ctx, "session", "run", 1, 0) }, turnEvents},
		{"single event", func() ([]Event, error) {
			event, err := st.RunEvent(ctx, "session", "run", originals[len(originals)-1].Sequence)
			return []Event{event}, err
		}, originals[len(originals)-1:]},
	} {
		t.Run(read.name, func(t *testing.T) {
			got, err := read.call()
			if err != nil || !reflect.DeepEqual(got, read.want) {
				t.Fatalf("event content/envelope changed: got %+v, want %+v, error %v", got, read.want, err)
			}
		})
	}
	t.Run("overview", func(t *testing.T) {
		if _, err := st.RunOverview(ctx, "session", "run"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("output links", func(t *testing.T) {
		links, err := st.outputLinks(ctx, "session", "artifact", []string{"artifact"})
		if err != nil || links["artifact"].CallID != "call" || links["artifact"].TaskID != "main" {
			t.Fatalf("output association lost: %+v %v", links, err)
		}
	})
	t.Run("tool artifact", func(t *testing.T) {
		got, err := st.ToolArtifact(ctx, "session", "run", "call")
		if err != nil || got.ID != "artifact" || got.TaskID != "main" || got.Status != "succeeded" {
			t.Fatalf("tool artifact lost: %+v %v", got, err)
		}
	})
}
