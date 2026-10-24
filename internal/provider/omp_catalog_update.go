package provider

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const ompCatalogRawURL = "https://raw.githubusercontent.com/can1357/oh-my-pi/main/packages/catalog/src/models.json"
const ompCatalogCDNURL = "https://cdn.jsdelivr.net/gh/can1357/oh-my-pi@main/packages/catalog/src/models.json"
const ompCatalogVersionURL = "https://api.github.com/repos/can1357/oh-my-pi/contents/packages/catalog/src/models.json"

type OMPCatalogInfo struct {
	Revision          string `json:"revision"`
	ModelCount        int    `json:"modelCount"`
	ProviderCount     int    `json:"providerCount"`
	UpdatedAt         string `json:"updatedAt,omitempty"`
	ModelsDevRevision string `json:"modelsDevRevision,omitempty"`
}

func (catalog *OMPCatalogSnapshot) Info() OMPCatalogInfo {
	if catalog == nil {
		return OMPCatalogInfo{}
	}
	info := OMPCatalogInfo{Revision: catalog.data.Revision, ProviderCount: len(catalog.models), UpdatedAt: catalog.data.UpdatedAt}
	for _, models := range catalog.models {
		info.ModelCount += len(models)
	}
	if catalog.data.ModelsDev != nil {
		info.ModelsDevRevision = catalog.data.ModelsDev.Revision
	}
	return info
}

// FetchModelCatalog stages both metadata sources without changing the active
// directory. Keep the source rows separate so the next update cannot retain
// removed models.dev rows or reuse an earlier merge as OMP's original data.
func FetchModelCatalog(ctx context.Context, client *http.Client) (*OMPCatalogSnapshot, error) {
	current := CurrentOMPCatalog()
	group, ctx := errgroup.WithContext(ctx)
	var omp *OMPCatalogSnapshot
	var modelsDev *modelsDevCatalog
	group.Go(func() (err error) {
		omp, err = FetchOMPCatalog(ctx, client)
		return err
	})
	group.Go(func() (err error) {
		modelsDev, err = fetchModelsDevCatalog(ctx, client)
		return err
	})
	if err := group.Wait(); err != nil {
		return nil, err
	}
	file := omp.data
	file.ModelsDev = modelsDev
	file.UpdatedAt = current.data.UpdatedAt
	if file.Revision != current.data.Revision || current.data.ModelsDev == nil || modelsDev.Revision != current.data.ModelsDev.Revision {
		file.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(file)
	if err != nil {
		return nil, err
	}
	return decodeOMPCatalog(data)
}

// FetchOMPCatalog resolves the authoritative file SHA, then checks every mirror
// against it. Git's blob hash binds the revision to the exact downloaded bytes,
// so a stale branch cache cannot replace the installed catalog.
func FetchOMPCatalog(ctx context.Context, client *http.Client) (*OMPCatalogSnapshot, error) {
	versionCtx, cancel := context.WithTimeout(ctx, catalogDownloadTimeout)
	data, err := fetchCatalogFile(versionCtx, client, ompCatalogVersionURL, "OMP", 512<<10)
	cancel()
	if err != nil {
		return nil, err
	}
	var version struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		return nil, errors.New("OMP returned an invalid catalog revision")
	}
	if decoded, err := hex.DecodeString(version.SHA); err != nil || len(decoded) != 20 {
		return nil, errors.New("OMP returned an invalid catalog revision")
	}
	if current := CurrentOMPCatalog(); current != nil && current.data.Revision == version.SHA {
		return current, nil
	}
	return fetchCatalogMirrors(ctx, "OMP", []string{ompCatalogRawURL, ompCatalogCDNURL}, func(ctx context.Context, url string) (*OMPCatalogSnapshot, error) {
		data, err := fetchCatalogFile(ctx, client, url, "OMP", 32<<20)
		if err != nil {
			return nil, err
		}
		if ompSourceRevision(data) != version.SHA {
			return nil, errors.New("OMP mirror content does not match the authoritative file SHA")
		}
		return decodeOMPSourceCatalog(data, version.SHA)
	})
}

func ompSourceRevision(data []byte) string {
	hash := sha1.New()
	fmt.Fprintf(hash, "blob %d\x00", len(data))
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func decodeOMPSourceCatalog(data []byte, revision string) (*OMPCatalogSnapshot, error) {
	// The source includes runtime/pricing fields. Keep exactly the same stable
	// metadata subset as scripts/build_omp_model_catalog.py.
	var source map[string]map[string]struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		OMPModel
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("decode OMP model data: %w", err)
	}
	file := ompCatalogFile{Source: "oh-my-pi", Revision: revision, UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Providers: make(map[string]map[string]OMPModel, len(source))}
	for providerID, models := range source {
		rows := make(map[string]OMPModel, len(models))
		for id, model := range models {
			if model.ID != id || model.Provider != providerID {
				return nil, errors.New("OMP model identity does not match its catalog entry")
			}
			model.ReasoningOptions = nil // Explicit controls belong to models.dev.
			rows[id] = model.OMPModel
		}
		if len(rows) != 0 {
			file.Providers[providerID] = rows
			file.ModelCount += len(rows)
		}
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return nil, err
	}
	return decodeOMPCatalog(encoded)
}

func OMPCatalogPath(dir string) string { return filepath.Join(dir, "omp-models.json") }

// Save replaces the last good on-disk snapshot only after a complete write.
// Activation is separate so the server can validate connections before saving.
func (catalog *OMPCatalogSnapshot) Save(dir string) error {
	data, err := json.Marshal(catalog.data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".omp-models-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), OMPCatalogPath(dir))
}

var ompDiskState struct {
	sync.Mutex
	path    string
	version OMPCatalogInfo
	modTime time.Time
	size    int64
}

// LoadOMPCatalog is used at server startup and at Worker execution boundaries.
// A stat avoids reparsing an unchanged catalog for every user message.
func LoadOMPCatalog(dir string) error {
	ompDiskState.Lock()
	defer ompDiskState.Unlock()
	path := OMPCatalogPath(dir)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxOMPCatalogBytes {
		return errors.New("invalid local OMP catalog file")
	}
	if path == ompDiskState.path && info.ModTime().Equal(ompDiskState.modTime) && info.Size() == ompDiskState.size && CurrentOMPCatalog().Info() == ompDiskState.version {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxOMPCatalogBytes+1))
	if err != nil {
		return err
	}
	catalog, err := decodeOMPCatalog(data)
	if err != nil {
		return err
	}
	catalog.Activate()
	ompDiskState.path, ompDiskState.version = path, catalog.Info()
	ompDiskState.modTime, ompDiskState.size = info.ModTime(), info.Size()
	return nil
}
