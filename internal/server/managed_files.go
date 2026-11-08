package server

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"vista/internal/configstore"
)

// Watch only managed roots and their immediate folders. Following symlinks or
// recursively watching user workspaces would make costs unrelated to Vista's
// inventory. Parent watches also catch directory replacement and atomic saves.
func (a *App) watchManagedFiles() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("managed file watcher: %v", err)
		return
	}
	defer watcher.Close()
	roots := map[string]string{filepath.Join(a.configDir, "skills"): "skills", filepath.Join(a.dir, "resources"): "storage"}
	reconcile := func() {
		wanted := map[string]bool{a.configDir: true, a.dir: true}
		for root := range roots {
			info, err := os.Lstat(root)
			if err != nil || !info.IsDir() {
				continue
			}
			wanted[root] = true
			entries, _ := os.ReadDir(root)
			for _, entry := range entries {
				if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
					wanted[filepath.Join(root, entry.Name())] = true
				}
			}
		}
		current := map[string]bool{}
		for _, path := range watcher.WatchList() {
			current[path] = true
			if !wanted[path] {
				_ = watcher.Remove(path)
			}
		}
		for path := range wanted {
			if !current[path] {
				if err := watcher.Add(path); err != nil && !os.IsNotExist(err) {
					log.Printf("watch managed directory %s: %v", path, err)
				}
			}
		}
	}
	reconcile()
	serverRevision := func() string {
		data, present, err := configstore.Read(a.configDir, configstore.Server)
		if err != nil {
			return "unavailable"
		}
		return configstore.Revision(data, present)
	}
	lastServerRevision := serverRevision()
	var flush <-chan time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	dirty := map[string]bool{}
	mark := func(topic string) {
		dirty[topic] = true
		// A fixed coalescing window also flushes continuous write bursts.
		if flush == nil {
			timer.Reset(150 * time.Millisecond)
			flush = timer.C
		}
	}
	for {
		select {
		case <-a.ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Name == filepath.Join(a.configDir, configstore.Filename) || event.Name == filepath.Join(a.configDir, "server.json") || event.Name == a.configDir {
				// Other section writes share the file but do not invalidate this
				// editor. Legacy events matter only before explicit migration.
				mark("server-config-check")
			}
			if !event.Has(fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename) {
				continue
			}
			for root, topic := range roots {
				if event.Name == root || strings.HasPrefix(event.Name, root+string(filepath.Separator)) {
					mark(topic)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// Overflow means revisions may have been missed. Reconcile inventory
			// and invalidate managed views rather than trusting the partial batch.
			log.Printf("managed file watcher: %v", err)
			mark("skills")
			mark("storage")
			mark("server-config")
		case <-flush:
			flush = nil
			reconcile()
			for topic := range dirty {
				if topic == "server-config-check" {
					current := serverRevision()
					if current != lastServerRevision {
						a.updates.notify("server-config")
						lastServerRevision = current
					}
					continue
				}
				if topic == "storage" {
					a.invalidateResourceStorage()
				} else {
					a.updates.notify(topic)
				}
			}
			clear(dirty)
		}
	}
}
