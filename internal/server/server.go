package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"vista/internal/appdirs"
	"vista/internal/buildinfo"
	"vista/internal/codexauth"
	"vista/internal/copilotauth"
	"vista/internal/instance"
	"vista/internal/modelcatalog"
	"vista/internal/modeltrace"
	"vista/internal/platform"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
	"vista/licenses"
)

//go:embed assets/app.js assets/vista-web.css
var webAssets embed.FS

type worker struct {
	cmd             *exec.Cmd
	owner           store.WorkerOwner
	input           io.WriteCloser
	writeMu         sync.Mutex
	runMu           sync.Mutex // Serializes dispatch with cancellation for this Worker.
	dispatchedRunID string
	stopping        bool
	exiting         bool
	done            chan struct{}
	metrics         workerproto.Metrics
	overBudget      bool
}
type App struct {
	background             sync.WaitGroup
	closeContext           context.CancelFunc
	sessionMutations       sync.Map // Per-session HTTP/queued-dispatch serialization.
	storageMu              sync.Mutex
	skillsMu               sync.Mutex
	agentSettingsMu        sync.Mutex
	storageStatus          resourceStorageStatus
	credentialCache        provider.CredentialCache
	mu                     sync.Mutex
	admissionMu            sync.Mutex
	workers                map[string]*worker
	workerOperations       map[string]*workerOperation
	workerStarting         int
	workersStopping        bool
	workerFailures         map[string]workerFailureWindow
	store                  *store.Store
	dir                    string
	configDir              string
	started                time.Time
	buildInfo              buildinfo.Metadata
	hostOS                 string
	ctx                    context.Context
	cancel                 context.CancelFunc
	updates                stateHub
	hub                    eventHub
	modelMu                sync.RWMutex
	ompCatalogUpdateMu     sync.Mutex
	ompCatalogClient       *http.Client
	ompCatalogStatusMu     sync.Mutex
	ompCatalogStatus       catalogUpdateStatus
	ompCatalogWake         chan struct{}
	providerLoginMu        sync.Mutex
	providerLogins         map[string]*providerLogin
	codexAuthClient        *codexauth.Client
	copilotAuthClient      *copilotauth.Client
	modelRevision          uint64
	modelDiscoverySequence uint64
	modelDiscoveries       map[string]uint64
	models                 modelCatalog
	baseModels             modelCatalog
	localModels            map[string][]configuredModel
	customInventories      map[string][]configuredModel
	copilotInventories     map[string][]configuredModel
	mcpMu                  sync.Mutex
	mcpServers             []workerproto.MCPServer
	mcpChecks              map[string]mcpCheck
	mcpLogins              map[string]*mcpLogin
	searchMu               sync.RWMutex
	searchProviders        []workerproto.SearchProvider
	searchConfig           searchSettingsConfig
}

func Run(portOverride int, onReady func(context.Context) func()) error {
	build, buildErr := buildinfo.Read()
	if buildErr != nil {
		log.Printf("binary hash unavailable: %v", buildErr)
	}
	configDir, err := appdirs.Config()
	if err != nil {
		return err
	}
	config, err := LoadRuntimeConfig(configDir, portOverride)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	dir, err := instance.Dir()
	if err != nil {
		return err
	}
	lock, err := instance.Lock(dir)
	if err != nil {
		if platform.IsLockBusy(err) {
			return fmt.Errorf("Vista is already running: %w", err)
		}
		return fmt.Errorf("lock Vista instance: %w", err)
	}
	defer instance.Unlock(lock)
	socket, err := net.Listen("tcp", config.Address())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", config.Address(), err)
	}
	listener := &runtimeListener{listener: socket}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func() (*App, error) {
		app, err := loadApp(ctx, cancel, configDir, dir, build)
		if err != nil {
			return nil, err
		}
		if err := app.start(); err != nil {
			app.closeContext()
			app.stopAll()
			app.background.Wait()
			_ = app.store.Close()
			return nil, err
		}
		return app, nil
	}
	app, err := open()
	if err != nil {
		return err
	}
	handler := newAppHandler(app, open)
	defer handler.close()
	srv := &http.Server{Handler: app.host(handler), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	handler.restart = func() (string, error) {
		// The saved port was deleted. Resolve startup defaults and external
		// environment again, rather than carrying the previous CLI launch port.
		config, err := LoadRuntimeConfig(configDir, 0)
		if err != nil {
			return "", err
		}
		address := "http://" + config.Address()
		if config.Address() != listener.Addr().String() {
			next, err := net.Listen("tcp", config.Address())
			if err != nil {
				return "", fmt.Errorf("listen after reset on %s: %w", config.Address(), err)
			}
			if err := instance.Write(dir, instance.Info{PID: os.Getpid(), Address: address}); err != nil {
				next.Close()
				return "", err
			}
			if err := listener.replace(next); err != nil {
				return "", err
			}
		}
		// All previous API work has drained; retire idle connections to the old
		// address. The reset response itself requests Connection: close.
		srv.SetKeepAlivesEnabled(false)
		srv.SetKeepAlivesEnabled(true)
		return address, nil
	}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(shutdown)
	}()
	address := "http://" + listener.Addr().String()
	if err := instance.Write(dir, instance.Info{PID: os.Getpid(), Address: address}); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(dir, "instance.json"))
	log.Printf("Vista listening on %s", address)
	if onReady != nil {
		if closeControls := onReady(ctx); closeControls != nil {
			defer closeControls()
		}
	}
	err = srv.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// loadApp opens one generation of user data. The HTTP listener and instance lock
// outlive it so a full reset can replace all configuration and state in place.
func loadApp(parent context.Context, cancel context.CancelFunc, configDir, dir string, build buildinfo.Metadata) (_ *App, err error) {
	st, err := store.Open(filepath.Join(dir, "vista.db"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = st.Close()
		}
	}()
	owners, err := st.WorkerOwners(context.Background())
	if err != nil {
		return nil, fmt.Errorf("read Worker ownership records: %w", err)
	}
	for _, owner := range owners {
		if err := platform.KillOwnedSession(owner.PID, owner.StartTime); err != nil && !errors.Is(err, platform.ErrIdentityMismatch) {
			return nil, fmt.Errorf("clean up previous Worker for session %s: %w", owner.SessionID, err)
		}
		if err := st.ClearWorkerOwner(context.Background(), owner); err != nil {
			return nil, fmt.Errorf("clear previous Worker ownership record: %w", err)
		}
	}
	if _, err := st.InterruptRunning(context.Background(), ""); err != nil {
		return nil, fmt.Errorf("reconcile interrupted executions: %w", err)
	}
	if _, err := st.InterruptJobs(context.Background(), "", "Server restarted before the background job result was confirmed"); err != nil {
		return nil, fmt.Errorf("reconcile background jobs: %w", err)
	}
	if _, err := st.InterruptArtifacts(context.Background(), "", "Server restarted before output completeness was confirmed"); err != nil {
		return nil, fmt.Errorf("reconcile output artifacts: %w", err)
	}
	ctx, closeContext := context.WithCancel(parent)
	defer func() {
		if err != nil {
			closeContext()
		}
	}()
	if err := provider.LoadOMPCatalog(configDir); err != nil {
		log.Printf("local OMP catalog unavailable; using bundled metadata: %v", err)
	}
	models, err := modelcatalog.Load(configDir)
	if err != nil {
		return nil, err
	}
	mcpServers, err := loadMCPConfig(configDir)
	if err != nil {
		return nil, err
	}
	searchConfigs, err := searchProviders()
	if err != nil {
		return nil, err
	}
	searchSettings, err := loadSearchSettings(configDir)
	if err != nil {
		return nil, err
	}
	app := &App{workers: map[string]*worker{}, workerFailures: map[string]workerFailureWindow{}, store: st, dir: dir, configDir: configDir, started: time.Now(), buildInfo: build, hostOS: platform.HostOS(), ctx: ctx, cancel: cancel, closeContext: closeContext, models: models, baseModels: models, localModels: map[string][]configuredModel{}, customInventories: map[string][]configuredModel{}, mcpServers: mcpServers, searchProviders: searchConfigs, searchConfig: searchSettings}
	app.initCatalogUpdates()
	return app, nil
}

func (a *App) start() error {
	items, err := a.store.List(a.ctx)
	if err != nil {
		return err
	}
	a.background.Go(a.cleanModelDiagnostics)
	a.background.Go(func() { a.pollModelCatalog(a.ctx) })
	a.refreshLocalModels(a.ctx)
	a.background.Go(func() { a.pollLocalModels(a.ctx) })
	a.background.Go(func() { a.refreshCustomModels(a.ctx) })
	a.background.Go(func() { a.pollCustomModels(a.ctx) })
	a.background.Go(func() { a.refreshCopilotModels(a.ctx) })
	a.background.Go(func() { a.pollCopilotModels(a.ctx) })
	for _, item := range items {
		if item.Open {
			if err := a.startWorker(item.ID); err != nil {
				log.Printf("restore worker %s: %v", item.ID, err)
			} else {
				a.background.Go(func() { a.launchQueuedInput(item.ID) })
			}
		}
	}
	return nil
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /licenses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, "THIRD_PARTY_NOTICES", time.Time{}, strings.NewReader(licenses.Text))
	})
	mux.HandleFunc("/api/events", a.stateEvents)
	mux.HandleFunc("/api/status", a.status)
	mux.HandleFunc("/api/resource-storage", a.resourceStorage)
	mux.HandleFunc("/api/models", a.modelList)
	mux.HandleFunc("/api/model-catalog", a.ompCatalog)
	mux.HandleFunc("/api/user-agent", a.userAgentSettings)
	mux.HandleFunc("/api/model-config", a.modelSettings)
	mux.HandleFunc("/api/provider-config", a.providerSettings)
	mux.HandleFunc("/api/provider-import", a.importProviders)
	mux.HandleFunc("/api/extension-import", a.importExtensions)
	mux.HandleFunc("/api/provider-auth/codex", a.startProviderLogin)
	mux.HandleFunc("/api/provider-auth/codex/", a.providerLoginStatus)
	mux.HandleFunc("/api/provider-auth/copilot", a.startProviderLogin)
	mux.HandleFunc("/api/provider-auth/copilot/", a.providerLoginStatus)
	mux.HandleFunc("/api/agent-settings", a.agentSettings)
	mux.HandleFunc("/api/memories", a.memories)
	mux.HandleFunc("/api/search-config", a.searchSettings)
	mux.HandleFunc("/api/mcp-config", a.mcpSettings)
	mux.HandleFunc("/api/mcp-servers/", a.mcpServerAction)
	mux.HandleFunc("/api/mcp-auth/", a.mcpLoginStatus)
	mux.HandleFunc("/api/skills", a.skillSettings)
	mux.HandleFunc("/api/skills/", a.skillSetting)
	mux.HandleFunc("/api/preferences", a.preferences)
	mux.HandleFunc("/api/model-selection/stats", a.modelSelectionStats)
	mux.HandleFunc("/api/model-discovery", a.discoverCustomModels)
	mux.HandleFunc("/api/zen-model-discovery", a.discoverZenModels)
	mux.HandleFunc("/api/stop", a.stop)
	mux.HandleFunc("/api/sessions", a.sessions)
	mux.HandleFunc("/api/sessions/", a.session)
	mux.HandleFunc("/app.js", a.asset)
	mux.HandleFunc("/vista-web.css", a.asset)
	mux.HandleFunc("/", a.root)
	return a.host(a.stateMutations(mux))
}
func (a *App) host(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" || (!strings.HasPrefix(r.Host, "127.0.0.1:") && !strings.HasPrefix(r.Host, "[::1]:")) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://"+r.Host {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *App) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method", 405)
		return
	}
	status, err := a.statusSnapshot(r.Context(), r.Host)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonOut(w, status)
}

func (a *App) statusSnapshot(ctx context.Context, host string) (map[string]any, error) {
	counts, err := a.store.SessionCounts(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	count := len(a.workers)
	workerMetrics := make([]map[string]any, 0, count)
	for id, worker := range a.workers {
		workerMetrics = append(workerMetrics, map[string]any{"sessionId": id, "pid": worker.cmd.Process.Pid, "memory": worker.metrics})
	}
	a.mu.Unlock()
	unavailable := counts.Open - count
	if unavailable < 0 {
		unavailable = 0
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	models := a.modelsSnapshot()
	defaultModel, _, _ := models.Resolve("")
	providerName := defaultModel.ProviderID
	if providerName == "" {
		providerName = defaultModel.Provider
	}
	defaultModelLabel := models.Default
	for _, item := range models.Models {
		if item.Key != models.Default || item.ProviderID != "custom" && item.Provider != "openai-chat" && item.Provider != "openai-responses" {
			continue
		}
		if item.Name != "" {
			providerName = item.Name
			defaultModelLabel = item.Name + "/" + item.ID
		}
		break
	}
	return map[string]any{"state": "running", "version": buildinfo.Version, "binaryHash": a.buildInfo.BinaryHash, "sourceCommit": a.buildInfo.SourceCommit, "sourceModified": a.buildInfo.SourceModified, "hostOS": a.hostOS, "address": "http://" + host, "provider": providerName, "model": defaultModel.ID, "defaultModel": defaultModelLabel, "defaultModelKey": models.Default, "uptimeSeconds": int(time.Since(a.started).Seconds()), "sessions": map[string]int{"total": counts.Total, "open": counts.Open, "closed": counts.Total - counts.Open, "running": counts.Running, "idle": counts.Open - counts.Running, "unavailable": unavailable, "workers": count}, "managedBackgroundProcesses": counts.Jobs, "eventStreams": a.hub.subscriberCount() + a.updates.count(), "eventStreamLimit": maxEventStreams + maxStateEventStreams, "memory": map[string]uint64{"heapBytes": mem.HeapAlloc, "rssBytes": platform.RSS(), "peakRssBytes": platform.PeakRSS()}, "workerMetrics": workerMetrics, "sampledAt": time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func (a *App) modelList(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method", 405)
		return
	}
	jsonOut(w, a.publicModelsSnapshot())
}
func (a *App) stop(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	w.WriteHeader(202)
	go a.cancel()
}
func (a *App) sessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		var filter []bool
		if value, present := r.URL.Query()["open"]; present {
			if len(value) != 1 || (value[0] != "true" && value[0] != "false") {
				http.Error(w, "open must be true or false", http.StatusBadRequest)
				return
			}
			filter = []bool{value[0] == "true"}
		}
		items, err := a.store.List(r.Context(), filter...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonOut(w, items)
	case "POST":
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var input struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		if len(input.Title) > 200 {
			http.Error(w, "title too long", 400)
			return
		}
		if input.Title == "" {
			input.Title = "New session"
		}
		id := uuid.NewString()
		item, err := a.store.Create(r.Context(), id, input.Title)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		item, err = a.initializeSessionSelection(r.Context(), id)
		if err != nil {
			_ = a.store.Delete(r.Context(), id)
			http.Error(w, "could not initialize session preferences", 500)
			return
		}
		if err := a.startWorker(id); err != nil {
			_ = a.store.Delete(r.Context(), id)
			http.Error(w, err.Error(), 503)
			return
		}
		w.WriteHeader(201)
		jsonOut(w, item)
	default:
		http.Error(w, "method", 405)
	}
}
func (a *App) session(w http.ResponseWriter, r *http.Request) {
	part := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	bits := strings.Split(part, "/")
	if len(bits) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := uuid.Parse(bits[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		unlock := a.lockSessionMutation(id.String())
		defer unlock()
	}
	item, err := a.store.Get(r.Context(), id.String())
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if len(bits) == 4 && bits[1] == "messages" && (bits[3] == "revert" && r.Method == http.MethodPost || bits[3] == "draft" && r.Method == http.MethodGet) {
		messageID, err := uuid.Parse(bits[2])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if bits[3] == "draft" {
			result, err := a.messageDraft(r.Context(), item.ID, messageID.String())
			if err != nil {
				revertHTTPError(w, err)
				return
			}
			jsonOut(w, result)
			return
		}
		a.revertMessage(w, r, item.ID, messageID.String())
		return
	}
	if len(bits) >= 3 && bits[1] == "inputs" {
		a.inputQueue(w, r, item.ID, bits[2:])
		return
	}
	if len(bits) == 3 && bits[1] == "resources" {
		a.sessionResource(w, r, item.ID, bits[2])
		return
	}
	if (len(bits) == 4 || len(bits) == 5) && bits[1] == "runs" && bits[3] == "diagnostics" && r.Method == http.MethodGet {
		id := ""
		if len(bits) == 5 {
			id = bits[4]
		}
		a.modelDiagnostics(w, r, item.ID, bits[2], id)
		return
	}
	if len(bits) == 4 && bits[1] == "runs" && bits[3] == "stop-agent" && r.Method == http.MethodPost {
		if !item.Open {
			http.Error(w, "session is closed", http.StatusConflict)
			return
		}
		a.stopAgent(w, r, item.ID, bits[2])
		return
	}
	if len(bits) == 2 && bits[1] == "selection" && r.Method == http.MethodPatch {
		a.sessionSelection(w, r, item.ID)
		return
	}
	if len(bits) == 2 && bits[1] == "feedback" && r.Method == http.MethodGet {
		a.sessionFeedback(w, r, item.ID)
		return
	}
	if len(bits) == 4 && bits[1] == "messages" && bits[3] == "feedback" && r.Method == http.MethodPut {
		a.messageFeedback(w, r, item.ID, bits[2])
		return
	}
	if len(bits) == 2 && bits[1] == "context" && r.Method == http.MethodGet {
		a.compactionStatus(w, r, item.ID)
		return
	}
	if len(bits) == 2 && bits[1] == "context-usage" && r.Method == http.MethodGet {
		a.contextUsage(w, r, item.ID)
		return
	}
	if len(bits) == 2 && bits[1] == "compact" && r.Method == http.MethodPost {
		a.compactSession(w, r, item.ID)
		return
	}
	if r.Method == http.MethodPatch && len(bits) == 1 {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var input struct {
			Title string `json:"title"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid title request", 400)
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		if input.Title == "" || len(input.Title) > 200 || strings.ContainsAny(input.Title, "\r\n") {
			http.Error(w, "title must be one line and at most 200 bytes", 400)
			return
		}
		if err := a.store.Rename(r.Context(), item.ID, input.Title); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		item.Title = input.Title
		jsonOut(w, item)
		return
	}
	if r.Method == "DELETE" && len(bits) == 1 {
		if err := a.store.SetOpen(r.Context(), item.ID, false); err != nil {
			http.Error(w, "could not close session", http.StatusInternalServerError)
			return
		}
		a.stopWorker(item.ID)
		paths, err := a.store.ArtifactPaths(r.Context(), item.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := a.store.Delete(r.Context(), item.ID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, path := range paths {
			_ = a.removeOutput(path)
		}
		_ = modeltrace.DeleteSession(a.dir, item.ID)
		w.WriteHeader(204)
		return
	}
	if len(bits) == 5 && bits[1] == "messages" && bits[3] == "images" && r.Method == "GET" {
		messageID, parseErr := uuid.Parse(bits[2])
		index, indexErr := strconv.Atoi(bits[4])
		if parseErr != nil || indexErr != nil || index < 0 {
			http.NotFound(w, r)
			return
		}
		image, err := a.store.MessageImage(r.Context(), item.ID, messageID.String(), index)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "could not read message image", 500)
			return
		}
		w.Header().Set("Content-Type", image.MediaType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(image.Data)
		return
	}
	if len(bits) == 2 && bits[1] == "artifacts" && r.Method == "GET" {
		artifacts, err := a.store.Artifacts(r.Context(), item.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonOut(w, artifacts)
		return
	}
	if len(bits) == 4 && bits[1] == "artifacts" && bits[3] == "output" && r.Method == "GET" {
		artifactID, parseErr := uuid.Parse(bits[2])
		if parseErr != nil {
			http.NotFound(w, r)
			return
		}
		artifact, err := a.store.GetArtifact(r.Context(), item.ID, artifactID.String())
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		a.outputPage(w, r, artifact.ID, artifact.OutputPath, artifact.Complete)
		return
	}
	if len(bits) == 2 && bits[1] == "jobs" && r.Method == "GET" {
		jobs, err := a.store.Jobs(r.Context(), item.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for i := range jobs {
			a.jobSize(&jobs[i])
		}
		jsonOut(w, jobs)
		return
	}
	if (len(bits) == 3 || len(bits) == 4) && bits[1] == "jobs" {
		jobID, parseErr := uuid.Parse(bits[2])
		if parseErr != nil {
			http.NotFound(w, r)
			return
		}
		job, jobErr := a.store.GetJob(r.Context(), item.ID, jobID.String())
		if errors.Is(jobErr, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if jobErr != nil {
			http.Error(w, jobErr.Error(), 500)
			return
		}
		if len(bits) == 3 {
			if r.Method != http.MethodGet {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			a.jobSize(&job)
			jsonOut(w, job)
			return
		}
		if bits[3] == "stop" && r.Method == "POST" {
			if job.Status != "starting" && job.Status != "running" {
				http.Error(w, "background job is not active", 409)
				return
			}
			if !item.Open || a.sendWorker(item.ID, workerproto.Command{Action: "stop_job", RunID: job.RunID, JobID: job.ID}) != nil {
				http.Error(w, "Worker is unavailable", 503)
				return
			}
			w.WriteHeader(202)
			return
		}
		if bits[3] == "output" && r.Method == "GET" {
			a.jobOutput(w, r, job)
			return
		}
	}
	if r.Method == http.MethodGet && (len(bits) == 5 && bits[1] == "runs" && bits[3] == "events" || len(bits) == 6 && bits[1] == "runs" && bits[3] == "turns" && bits[5] == "events") {
		runID, err := uuid.Parse(bits[2])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if len(bits) == 5 {
			a.runEvent(w, r, item.ID, runID.String(), bits[4])
		} else {
			a.runTurnEvents(w, r, item.ID, runID.String(), bits[4])
		}
		return
	}
	if len(bits) == 4 && bits[1] == "runs" && (bits[3] == "overview" || bits[3] == "main-outputs") && r.Method == "GET" {
		runID, err := uuid.Parse(bits[2])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if bits[3] == "main-outputs" {
			a.mainOutputs(w, r, item.ID, runID.String())
		} else {
			a.runOverview(w, r, item.ID, runID.String())
		}
		return
	}
	if len(bits) == 4 && bits[1] == "runs" && bits[3] == "model-output" && r.Method == "GET" {
		runID, parseErr := uuid.Parse(bits[2])
		if parseErr != nil {
			http.NotFound(w, r)
			return
		}
		turn, turnErr := strconv.ParseInt(r.URL.Query().Get("turn"), 10, 32)
		if turnErr != nil || turn <= 0 {
			http.Error(w, "invalid model turn", 400)
			return
		}
		offset, full, err := parseOutputRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var page store.ModelOutputPage
		if full {
			page, err = a.store.ReadFullModelOutput(r.Context(), item.ID, runID.String(), int32(turn))
		} else {
			page, err = a.store.ReadModelOutput(r.Context(), item.ID, runID.String(), int32(turn), offset, 4096)
		}
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, store.ErrModelOutputRange) {
			http.Error(w, "invalid model output range", 416)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, page)
		return
	}
	if r.Method != "POST" || len(bits) != 2 {
		if r.Method == "GET" && len(bits) == 2 {
			switch bits[1] {
			case "checkpoint":
				point, err := a.store.Checkpoint(r.Context(), item.ID)
				if errors.Is(err, sql.ErrNoRows) {
					http.NotFound(w, r)
					return
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Cache-Control", "no-store")
				jsonOut(w, point)
				return
			case "snapshot":
				a.conversationSnapshot(w, r, item.ID)
				return
			case "messages":
				a.messages(w, r, item.ID)
				return
			case "inputs":
				inputs, err := a.store.RecentInputs(r.Context(), item.ID)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				jsonOut(w, inputs)
				return
			case "events":
				a.eventStream(w, r, item.ID)
				return
			case "event-page":
				a.eventPage(w, r, item.ID)
				return
			}
		}
		http.Error(w, "method", 405)
		return
	}
	switch bits[1] {
	case "read":
		a.readSession(w, r, item.ID)
		return
	case "messages":
		a.submitMessage(w, r, item.ID)
		return
	case "cancel":
		if !item.Open {
			http.Error(w, "session is closed", 409)
			return
		}
		runID, lookupErr := a.store.RunningRunID(r.Context(), item.ID)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			http.Error(w, "session is idle", 409)
			return
		}
		if lookupErr != nil {
			http.Error(w, "could not find running execution", 500)
			return
		}
		if err := a.store.MarkQueuedInputs(r.Context(), item.ID, "", "interrupted"); err != nil {
			http.Error(w, "could not pause pending inputs", 500)
			return
		}
		a.notifySession(item.ID, "sessions")
		if sendErr := a.cancelRun(r.Context(), item.ID, runID); sendErr != nil {
			http.Error(w, "Worker is unavailable", 503)
			return
		}
		w.WriteHeader(202)
		return
	case "close":
		err = a.store.SetOpen(r.Context(), item.ID, false)
		if err == nil {
			a.stopWorker(item.ID)
		}
	case "open":
		if !item.Open {
			if err = a.startWorker(item.ID); err == nil {
				err = a.store.SetOpen(r.Context(), item.ID, true)
			}
		}
	case "retry":
		if !item.Open {
			http.Error(w, "session is closed", 409)
			return
		}
		a.mu.Lock()
		if a.workerFailures != nil {
			delete(a.workerFailures, item.ID)
		}
		a.mu.Unlock()
		err = a.startWorker(item.ID)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	w.WriteHeader(204)
}

func (a *App) jobOutput(w http.ResponseWriter, r *http.Request, job store.Job) {
	a.outputPage(w, r, job.ID, job.OutputPath, job.Status != "starting" && job.Status != "running")
}

func validOutputPath(stateDir, outputPath string) bool {
	root := filepath.Join(stateDir, "outputs")
	rel, err := filepath.Rel(root, outputPath)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (a *App) openOutput(outputPath string) (*os.File, error) {
	if !validOutputPath(a.dir, outputPath) {
		return nil, errors.New("invalid output path")
	}
	rootPath := filepath.Join(a.dir, "outputs")
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(rootPath, outputPath)
	if err != nil {
		return nil, err
	}
	return root.Open(rel)
}

func (a *App) removeOutput(outputPath string) error {
	if !validOutputPath(a.dir, outputPath) {
		return errors.New("invalid output path")
	}
	rootPath := filepath.Join(a.dir, "outputs")
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(rootPath, outputPath)
	if err != nil {
		return err
	}
	return root.Remove(rel)
}

func (a *App) outputPage(w http.ResponseWriter, r *http.Request, id, outputPath string, complete bool) {
	if !validOutputPath(a.dir, outputPath) {
		http.Error(w, "invalid output path", 500)
		return
	}
	offset, full, err := parseOutputRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, err := a.openOutput(outputPath)
	if err != nil {
		http.Error(w, "output unavailable", 404)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || offset > info.Size() {
		http.Error(w, "invalid output range", 416)
		return
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 65536 || full {
			http.Error(w, "invalid output limit", http.StatusBadRequest)
			return
		}
		a.outputRange(w, file, id, offset, limit, info.Size(), complete)
		return
	}
	length := min(int64(4096), info.Size()-offset)
	if full {
		length = info.Size()
	}
	buf := make([]byte, length)
	n, err := file.ReadAt(buf, offset)
	if (err != nil && err != io.EOF) || int64(n) != length {
		http.Error(w, "output read failed", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{"id": id, "jobId": id, "offset": offset, "nextOffset": offset + length, "availableBytes": info.Size(), "content": strings.ToValidUTF8(string(buf), "�"), "complete": complete})
}
func (a *App) root(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if (path != "/" && path != "/settings" && path != "/settings/models" && path != "/settings/about" && path != "/settings/general" && path != "/settings/personalization" && path != "/settings/search" && path != "/settings/storage" && path != "/settings/mcp" && path != "/settings/skills" && path != "/sessions" && !strings.HasPrefix(path, "/sessions/")) || r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(path, "/sessions/") {
		if _, err := uuid.Parse(strings.TrimPrefix(path, "/sessions/")); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Vista</title><script>try{const p=localStorage.getItem('vista-appearance');document.documentElement.dataset.theme=p==='light'||p==='dark'?p:matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'}catch{document.documentElement.dataset.theme=matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'}</script><link rel="stylesheet" href="/vista-web.css"></head><body><div id="app">Loading Vista…</div><script src="/app.js"></script></body></html>`)
}
func (a *App) asset(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method", 405)
		return
	}
	name := "assets" + r.URL.Path
	b, err := webAssets.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "application/javascript")
	} else {
		w.Header().Set("Content-Type", "text/css")
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}
