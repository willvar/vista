package server

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"vista/internal/store"
)

func parsePagination(query url.Values) (store.Pagination, bool, error) {
	page := store.Pagination{Page: 1, PageSize: store.DefaultPageSize}
	present := query.Has("page") || query.Has("pageSize")
	for name, target := range map[string]*int{"page": &page.Page, "pageSize": &page.PageSize} {
		if values, exists := query[name]; exists {
			value, err := strconv.Atoi(query.Get(name))
			if len(values) != 1 || err != nil || value < 1 || name == "pageSize" && value > store.MaxPageSize {
				return page, present, fmt.Errorf("invalid %s", name)
			}
			*target = value
		}
	}
	return page, present, nil
}

// Capture before reading a page, so a concurrent commit is never incorrectly
// acknowledged by an older snapshot. The epoch prevents reuse after restart.
func (a *App) stateVersion(topic string) string {
	point := a.updates.snapshot()
	return point.Epoch + ":" + strconv.FormatUint(point.Revisions[topic], 10)
}

func (a *App) sessionPage(ctx context.Context, page store.Pagination, search store.SessionSearch, open ...bool) (store.SessionPage, error) {
	if len(open) == 1 && open[0] {
		search.IncludeSequences = true
		if page.Page == 1 {
			a.mu.Lock()
			search.AttentionWorkers = make([]string, 0, len(a.workers))
			for id := range a.workers {
				search.AttentionWorkers = append(search.AttentionWorkers, id)
			}
			a.mu.Unlock()
		}
	}
	return a.store.SessionPage(ctx, page, search, open...)
}
