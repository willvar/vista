package server

import (
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"vista/internal/store"
)

func (a *App) jobSize(job *store.Job) {
	if job.Status != "starting" && job.Status != "running" {
		return
	}
	file, err := a.openOutput(job.OutputPath)
	if err != nil {
		return
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil {
		job.Bytes = info.Size()
	}
}

// Fixed byte blocks meet at the next UTF-8 boundary. No rune is duplicated or
// lost when adjacent blocks are read independently or a live writer appends.
func (a *App) outputRange(w http.ResponseWriter, file *os.File, id string, offset, limit, available int64, complete bool) {
	data := make([]byte, min(limit+utf8.UTFMax-1, available-offset))
	n, err := file.ReadAt(data, offset)
	if err != nil && err != io.EOF || n != len(data) {
		http.Error(w, "output read failed", http.StatusInternalServerError)
		return
	}
	start, end := 0, min(int(limit), len(data))
	for start < len(data) && start < utf8.UTFMax-1 && !utf8.RuneStart(data[start]) {
		start++
	}
	for end < len(data) && !utf8.RuneStart(data[end]) {
		end++
	}
	if !complete && offset+int64(end) == available {
		for tail := max(start, end-utf8.UTFMax+1); tail < end; tail++ {
			if utf8.RuneStart(data[tail]) && !utf8.FullRune(data[tail:end]) {
				end = tail
				break
			}
		}
	}
	end = max(start, end)
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{"id": id, "jobId": id, "offset": offset + int64(start), "nextOffset": offset + int64(end), "availableBytes": available, "content": strings.ToValidUTF8(string(data[start:end]), "�"), "complete": complete})
}
