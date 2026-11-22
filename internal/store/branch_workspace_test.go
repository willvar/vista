package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"vista/internal/question"
)

func TestBranchWorkspacePreferenceAndGroupActivity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.db")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(path)
	must(err)
	t.Cleanup(func() { s.Close() })
	root, child, sibling := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err = s.Create(ctx, root, "Workspace")
	must(err)
	_, _, err = s.BeginRun(ctx, root, "prefix", "first", "shared history")
	must(err)
	_, err = s.FinishRun(ctx, root, "prefix", "prefix-reply", "succeeded", "shared reply")
	must(err)
	point, _, err := s.BeginRun(ctx, root, "point", "point-user", "branch here")
	must(err)
	_, err = s.FinishRun(ctx, root, "point", "root-reply", "succeeded", "root reply")
	must(err)
	_, err = s.MarkRead(ctx, root, 1<<20)
	must(err)
	_, err = s.Fork(ctx, root, child, point.ID)
	must(err)
	_, err = s.Fork(ctx, root, sibling, point.ID)
	must(err)
	group := func(entry string) SessionBranchGroup {
		t.Helper()
		list, err := s.ListSnapshot(ctx, true)
		if err != nil || len(list.Items) != 1 || list.Items[0].ID != root || list.Items[0].BranchGroup == nil {
			t.Fatalf("expected one root group: %+v, %v", list, err)
		}
		result := *list.Items[0].BranchGroup
		if result.EntryID != entry {
			t.Fatalf("entry=%s, want %s", result.EntryID, entry)
		}
		return result
	}
	if g := group(root); g.UnreadID != "" {
		t.Fatalf("inherited reply was counted as new activity: %+v", g)
	}
	if changed, err := s.PreferBranch(ctx, child); err != nil || !changed {
		t.Fatalf("save preference: changed=%t, err=%v", changed, err)
	}
	if changed, err := s.PreferBranch(ctx, child); err != nil || changed {
		t.Fatalf("duplicate preference: changed=%t, err=%v", changed, err)
	}
	must(s.Close())
	s, err = Open(path)
	must(err)
	group(child)
	_, _, err = s.BeginRun(ctx, child, "child-run", "child-user", "work")
	must(err)
	if g := group(child); g.ExecutingID != child || g.RunningID != child {
		t.Fatalf("group did not show its running branch: %+v", g)
	}
	checkpoint, err := s.Checkpoint(ctx, root)
	must(err)
	if checkpoint.Session.Executing || checkpoint.Session.BranchGroup != nil {
		t.Fatalf("root execution was confused with group activity: %+v", checkpoint.Session)
	}
	must(s.SetOpen(ctx, root, false))
	page, err := s.SessionPage(ctx, Pagination{Page: 1, PageSize: 10}, SessionSearch{}, true)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Open || page.Items[0].BranchGroup.ExecutingID != child {
		t.Fatalf("open child of closed root was hidden by pagination: %+v, %v", page, err)
	}
	questions := []question.Question{{Question: "Which?", Options: []question.Option{{Label: "A"}}}}
	pending := &PendingReply{CallID: "ask", Kind: "question", Content: question.Format(questions), Questions: questions}
	_, err = s.FinishRunWithPending(ctx, child, "child-run", "child-reply", "succeeded", pending.Content, pending)
	must(err)
	if g := group(child); g.ExecutingID != "" || g.UnreadID != child || g.AwaitingID != child {
		t.Fatalf("group didn't switch from executing to attention: %+v", g)
	}
	checkpoint, err = s.Checkpoint(ctx, child)
	must(err)
	if checkpoint.Session.BranchRootID != root {
		t.Fatalf("expected root on a derived checkpoint: %+v", checkpoint.Session)
	}
	_, err = s.MarkRead(ctx, child, checkpoint.Session.LatestReplySequence)
	must(err)
	if g := group(child); g.UnreadID != "" || g.AwaitingID != child {
		t.Fatalf("reading a reply must not answer a question: %+v", g)
	}
	must(s.SetOpen(ctx, child, false))
	group(sibling)
	if _, err := s.PreferBranch(ctx, child); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed branch accepted: %v", err)
	}
	must(s.SetOpen(ctx, child, true))
	group(child) // Falling back never rewrites the stored preference.
	must(s.Delete(ctx, child))
	group(sibling)
	must(s.SetOpen(ctx, root, true))
	group(root)
}
