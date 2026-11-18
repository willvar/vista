package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func (a *App) connectionStatistics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", 405)
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || (days != 7 && days != 30 && days != 90) {
		http.Error(w, "invalid statistics period", 400)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" || len(key) > 512 {
		http.Error(w, "invalid model connection", 400)
		return
	}
	model, selected, err := a.modelsSnapshot().Resolve(key)
	if err != nil {
		// Service-connection cards can show a saved anchor before its discovered
		// model rows become available. Keep its usage visible without resolving it.
		snapshot, settingsErr := a.modelSettingsSnapshot()
		if settingsErr == nil {
			for _, anchor := range snapshot.raw.Models {
				if anchor.Key == key {
					model.ConnectionID = anchor.ConnectionID
					selected = key
					break
				}
			}
		}
		if selected == "" {
			http.NotFound(w, r)
			return
		}
	}
	model.Key = selected
	now := time.Now()
	version := a.stateVersion("connection-statistics")
	groups, truncated, err := a.store.ConnectionUsage(r.Context(), model.UsageConnection(), days, now)
	if err != nil {
		http.Error(w, "could not read connection statistics", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{"groups": groups, "truncated": truncated, "days": days, "sampledAt": now.UTC(), "version": version})
}

func (a *App) recordCallUsage(sample modelcfg.CallUsage) {
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	changed, err := a.store.RecordCallUsage(ctx, sample, time.Now())
	if err != nil {
		log.Printf("could not save connection usage: %v", err)
		return
	}
	if changed {
		a.updates.notify("connection-statistics")
	}
}

func (c *workerResultConsumer) callUsage(result workerproto.Result) error {
	if result.CallUsage == nil {
		return errors.New("missing model call usage")
	}
	changed, err := c.store.RecordCallUsage(c.ctx, *result.CallUsage, time.Now())
	if err == nil && changed && c.changed != nil {
		c.changed("call_usage")
	}
	return err
}
