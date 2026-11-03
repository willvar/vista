package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func (a *App) statistics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	days := 30
	if value := r.URL.Query().Get("days"); value != "" {
		var err error
		days, err = strconv.Atoi(value)
		if err != nil || days != 7 && days != 30 && days != store.StatisticsRetentionDays {
			http.Error(w, "invalid statistics period", http.StatusBadRequest)
			return
		}
	}
	zone := r.URL.Query().Get("timezone")
	if zone == "" {
		zone = "UTC"
	}
	if len(zone) > 100 {
		http.Error(w, "invalid statistics timezone", http.StatusBadRequest)
		return
	}
	if _, err := time.LoadLocation(zone); err != nil {
		http.Error(w, "invalid statistics timezone", http.StatusBadRequest)
		return
	}
	version := a.stateVersion("statistics")
	stats, err := a.store.Statistics(r.Context(), days, zone, time.Now())
	if err != nil {
		http.Error(w, "could not read statistics", http.StatusInternalServerError)
		return
	}
	jsonOut(w, struct {
		store.Statistics
		Version string `json:"version"`
	}{stats, version})
}

func (a *App) activity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Minutes []int64 `json:"minutes"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		http.Error(w, "invalid activity report", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid activity report", http.StatusBadRequest)
		return
	}
	now := time.Now()
	// Accept small client clock skew by clamping the next minute, never storing
	// activity in the future or letting it expand the user's active time.
	for i, minute := range input.Minutes {
		if minute == now.Unix()/60+1 {
			input.Minutes[i] = now.Unix() / 60
		}
	}
	changed, err := a.store.RecordActivity(r.Context(), input.Minutes, now)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrInvalidActivity) {
			status = http.StatusBadRequest
		}
		http.Error(w, "could not record activity", status)
		return
	}
	if changed {
		a.updates.notify("statistics")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) recordTokenUsage(id string, tokens *int64) {
	// A completed request remains consumed even if its title or conversation is
	// no longer eligible for saving. Do not inherit cancellation from that work.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changed, err := a.store.RecordTokenUsage(ctx, id, tokens, time.Now())
	if err != nil {
		log.Printf("could not save token usage: %v", err)
		return
	}
	if changed {
		a.updates.notify("statistics")
	}
}

func (c *workerResultConsumer) tokenUsage(result workerproto.Result) error {
	usage := result.TokenUsage
	if usage == nil {
		return errors.New("missing token usage")
	}
	if _, err := uuid.Parse(usage.ID); err != nil {
		return errors.New("invalid token usage identity")
	}
	changed, err := c.store.RecordTokenUsage(c.ctx, usage.ID, usage.Tokens, time.Now())
	if err != nil {
		return err
	}
	if changed && c.changed != nil {
		c.changed("token_usage")
	}
	return nil
}
