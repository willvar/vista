package server

import (
	"net/http"
	"strings"

	"vista/internal/useragent"
)

func (a *App) userAgentSettings(w http.ResponseWriter, r *http.Request) {
	var config useragent.Config
	switch r.Method {
	case http.MethodGet:
		var err error
		config, err = useragent.Read(a.configDir)
		if err != nil {
			http.Error(w, "could not read User-Agent settings", http.StatusInternalServerError)
			return
		}
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := decodeSettingsRequest(r.Body, &config); err != nil {
			http.Error(w, "invalid User-Agent settings request", http.StatusBadRequest)
			return
		}
		if err := config.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.Custom = strings.TrimSpace(config.Custom)
		if err := useragent.Write(a.configDir, config); err != nil {
			http.Error(w, "could not save User-Agent settings", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, struct {
		useragent.Config
		Value     string             `json:"value"`
		Presets   []useragent.Preset `json:"presets"`
		MaxLength int                `json:"maxLength"`
	}{config, config.Value(), useragent.Presets(), useragent.MaxLength})
}
