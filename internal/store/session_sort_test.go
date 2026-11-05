package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionSortAppliesBeforePaginationAndKeepsPinsFirst(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for i := range 13 {
		id := fmt.Sprintf("s%02d", i)
		if _, err := s.Create(ctx, id, "Sort task "+id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE sessions SET created_at=? WHERE id=?", fmt.Sprintf("2026-01-%02dT00:00:00Z", i+1), id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"s00", "s02"} {
		if err := s.SetPinned(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	for id, timestamp := range map[string]string{
		"s01": "2026-02-01T12:00:00.1Z",
		"s03": "2026-02-01T12:00:00Z",
		"s04": "2026-02-02T00:00:00Z",
	} {
		if _, err := s.db.Exec("INSERT INTO events(session_id,sequence,kind,data,created_at) VALUES(?,1,'session_renamed','{}',?)", id, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		sort SessionSort
		want []string
	}{
		{"", []string{"s02", "s00", "s12", "s11", "s10", "s09", "s08", "s07", "s06", "s05", "s04", "s03", "s01"}},
		{SessionCreatedNewest, []string{"s02", "s00", "s12", "s11", "s10", "s09", "s08", "s07", "s06", "s05", "s04", "s03", "s01"}},
		{SessionCreatedOldest, []string{"s00", "s02", "s01", "s03", "s04", "s05", "s06", "s07", "s08", "s09", "s10", "s11", "s12"}},
		{SessionUpdatedNewest, []string{"s02", "s00", "s04", "s01", "s03", "s12", "s11", "s10", "s09", "s08", "s07", "s06", "s05"}},
		{SessionUpdatedOldest, []string{"s00", "s02", "s05", "s06", "s07", "s08", "s09", "s10", "s11", "s12", "s03", "s01", "s04"}},
	} {
		t.Run(string(tc.sort), func(t *testing.T) {
			var ids []string
			for current := 1; current <= 3; current++ {
				page, err := s.SessionPage(ctx, Pagination{Page: current, PageSize: 5}, SessionSearch{Sort: tc.sort})
				if err != nil || page.Total != 13 || page.Page != current {
					t.Fatalf("page %d: %+v %v", current, page, err)
				}
				for _, item := range page.Items {
					ids = append(ids, item.ID)
					if (item.UpdatedAt != "") != tc.sort.byUpdate() {
						t.Fatalf("recent update timestamps must be scoped to update sorting: %+v", item)
					}
				}
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
		})
	}
	if err := s.SetOpen(ctx, "s01", false); err != nil {
		t.Fatal(err)
	}
	page, err := s.SessionPage(ctx, Pagination{}, SessionSearch{Text: "task s01", Sort: SessionUpdatedNewest}, false)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "s01" || page.Items[0].UpdatedAt != "2026-02-01T12:00:00.1Z" {
		t.Fatalf("filtered sort: %+v %v", page, err)
	}
	for _, err := range []error{
		s.SetOpen(ctx, "s01", true), s.SetPinned(ctx, "s01", true),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.MarkRead(ctx, "s01", 100); err != nil {
		t.Fatal(err)
	}
	page, err = s.SessionPage(ctx, Pagination{}, SessionSearch{Text: "task s01", Sort: SessionUpdatedNewest})
	if err != nil || len(page.Items) != 1 || page.Items[0].UpdatedAt != "2026-02-01T12:00:00.1Z" {
		t.Fatalf("read/pin/open changed recent update: %+v %v", page, err)
	}
	if err := s.Rename(ctx, "s01", "Renamed content"); err != nil {
		t.Fatal(err)
	}
	page, err = s.SessionPage(ctx, Pagination{}, SessionSearch{Text: "Renamed", Sort: SessionUpdatedNewest})
	if err != nil || len(page.Items) != 1 || page.Items[0].UpdatedAt <= "2026-02-01T12:00:00.1Z" {
		t.Fatalf("rename did not update activity: %+v %v", page, err)
	}
	if _, err := s.SessionPage(ctx, Pagination{}, SessionSearch{Sort: "invalid"}); err == nil {
		t.Fatal("accepted invalid sort")
	}
	items, err := s.List(ctx)
	if err != nil || len(items) != 13 || items[0].ID != "s02" || items[1].ID != "s01" || items[0].UpdatedAt != "" {
		t.Fatalf("manager sorting changed the sidebar list: %+v %v", items, err)
	}
}
