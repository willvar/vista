package store

import (
	"context"
	"fmt"
	"testing"
)

func TestSessionPagesReachArchivesBeyondLegacyLimitAndSearchBeforePaging(t *testing.T) {
	s := revertStore(t)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 1005 {
		title := fmt.Sprintf("Archive %04d", i)
		if i == 0 {
			title = "ÉTÉ 100%_报告"
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,title,is_open,created_at) VALUES(?,?,0,'2026-01-01T00:00:00Z')", fmt.Sprintf("archive-%04d", i), title); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for current := 1; current <= 11; current++ {
		page, err := s.SessionPage(ctx, Pagination{Page: current, PageSize: 100}, SessionSearch{}, false)
		if err != nil || page.Total != 1005 || page.Page != current || len(page.Items) != min(100, 1005-(current-1)*100) {
			t.Fatalf("page %d: %+v %v", current, page, err)
		}
		for _, item := range page.Items {
			if item.Open || seen[item.ID] {
				t.Fatalf("wrong scope or duplicate item: %+v", item)
			}
			seen[item.ID] = true
		}
	}
	if !seen["archive-0000"] || len(seen) != 1005 {
		t.Fatal("oldest sessions remain unreachable")
	}
	for _, term := range []string{"été", "100%_", "报告"} {
		page, err := s.SessionPage(ctx, Pagination{Page: 999, PageSize: 10}, SessionSearch{Text: term}, false)
		if err != nil || page.Page != 1 || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "archive-0000" {
			t.Fatalf("search %q: %+v %v", term, page, err)
		}
	}
	page, err := s.SessionPage(ctx, Pagination{Page: 2, PageSize: 20}, SessionSearch{Text: "no such conversation"}, true)
	if err != nil || page.Page != 1 || page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty page: %+v %v", page, err)
	}
	if err := s.Rename(ctx, "s", "New session"); err != nil {
		t.Fatal(err)
	}
	page, err = s.SessionPage(ctx, Pagination{}, SessionSearch{Text: "新会话", UntitledLabel: "新会话"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "s" {
		t.Fatalf("localized default title search: %+v %v", page, err)
	}
}
