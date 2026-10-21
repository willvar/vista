package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/store"
	"vista/internal/workerproto"
)

type contextNoticeAckSink struct {
	bytes.Buffer
	beforeWrite func([]byte) error
}

func (s *contextNoticeAckSink) Write(data []byte) (int, error) {
	if err := s.beforeWrite(data); err != nil {
		return 0, err
	}
	return s.Buffer.Write(data)
}
func (*contextNoticeAckSink) Close() error { return nil }

func TestWorkerContextNoticeIsCommittedBeforeAcknowledgment(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(ctx, "session", "compaction"); err != nil {
		t.Fatal(err)
	}
	_, initial, err := db.BeginRun(ctx, "session", "run", "user", "work")
	if err != nil {
		t.Fatal(err)
	}
	result := workerproto.Result{RunID: "run", Kind: "context_notice", Status: "succeeded", Turn: 0, Depth: 1, Content: "Completed work and constraints.", AckID: "ack-context"}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	sink := &contextNoticeAckSink{beforeWrite: func(data []byte) error {
		var command workerproto.Command
		if err := json.Unmarshal(data, &command); err != nil {
			return err
		}
		if command.Action != "ack" || command.RunID != result.RunID || command.AckID != result.AckID {
			return fmt.Errorf("wrong acknowledgment: %+v", command)
		}
		events, err := db.EventsAfter(ctx, "session", initial.Sequence, 10)
		if err != nil {
			return err
		}
		if len(events) != 1 || events[0].Kind != "context_notice" {
			return fmt.Errorf("acknowledgment arrived before notice persisted: %+v", events)
		}
		var notice struct {
			RunID   string `json:"runId"`
			Message string `json:"message"`
			Summary string `json:"summary"`
			Turn    int32  `json:"turn"`
			Depth   int    `json:"depth"`
		}
		if err := json.Unmarshal(events[0].Data, &notice); err != nil {
			return err
		}
		if notice.RunID != result.RunID || notice.Message != "Completed execution history was compacted; original tool records remain available" || notice.Summary != result.Content || notice.Turn != result.Turn || notice.Depth != result.Depth {
			return fmt.Errorf("persisted context notice changed: %+v", notice)
		}
		return nil
	}}
	w := &worker{cmd: &exec.Cmd{}, input: sink, stopping: true, done: make(chan struct{})}
	app := &App{ctx: ctx, store: db, dir: t.TempDir(), workers: map[string]*worker{"session": w}}
	app.readWorkerOutput("session", w, bufio.NewScanner(bytes.NewReader(append(encoded, '\n'))))
	if sink.Len() == 0 {
		t.Fatal("persisted notice was not acknowledged")
	}
}

func TestWorkerRejectsInvalidOrInactiveContextNotice(t *testing.T) {
	for _, test := range []struct {
		name     string
		modify   func(*workerproto.Result)
		inactive bool
	}{
		{"empty summary", func(result *workerproto.Result) { result.Content = "" }, false},
		{"oversized summary", func(result *workerproto.Result) { result.Content = strings.Repeat("x", (8<<10)+1) }, false},
		{"missing acknowledgment ID", func(result *workerproto.Result) { result.AckID = "" }, false},
		{"failed status", func(result *workerproto.Result) { result.Status = "failed" }, false},
		{"invalid turn", func(result *workerproto.Result) { result.Turn = -1 }, false},
		{"invalid depth", func(result *workerproto.Result) { result.Depth = -1 }, false},
		{"inactive run", func(*workerproto.Result) {}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Create(ctx, "session", "compaction"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginRun(ctx, "session", "run", "user", "work"); err != nil {
				t.Fatal(err)
			}
			if test.inactive {
				if _, err := db.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
					t.Fatal(err)
				}
			}
			result := workerproto.Result{RunID: "run", Kind: "context_notice", Status: "succeeded", Turn: 30, Depth: 0, Content: "summary", AckID: "ack-context"}
			test.modify(&result)
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			sink := &imageCommandSink{}
			w := &worker{cmd: &exec.Cmd{}, input: sink, stopping: true, done: make(chan struct{})}
			app := &App{ctx: ctx, store: db, dir: t.TempDir(), workers: map[string]*worker{"session": w}}
			app.readWorkerOutput("session", w, bufio.NewScanner(bytes.NewReader(append(encoded, '\n'))))
			if sink.Len() != 0 {
				t.Fatal("invalid context notice was acknowledged")
			}
			events, err := db.EventsAfter(ctx, "session", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Kind == "context_notice" {
					t.Fatalf("invalid context notice persisted: %s", event.Data)
				}
			}
		})
	}
}
