package resources_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/willvar/dofs"
	"vista/internal/configstore"
	"vista/internal/resources"
	"vista/internal/resourcetest"
	"vista/internal/store"
)

type storageFixture struct {
	Store   *store.Store
	Library resources.Library
	Config  resources.BackendConfig
	S3      *resourcetest.S3
	Path    string
}

func newStorageFixture(t *testing.T) *storageFixture {
	t.Helper()
	root, resolveErr := filepath.EvalSymlinks(t.TempDir())
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &storageFixture{Store: db, Library: resources.Library{StateDir: state}, Path: filepath.Join(state, "vista.db")}
	t.Cleanup(func() { f.Store.Close() })
	f.S3 = resourcetest.NewS3(t)
	key, err := dofs.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer dofs.Clear(key)
	f.Config = resources.BackendConfig{ID: uuid.NewString(), Kind: "s3", Endpoint: f.S3.Server.URL, Bucket: "resources", Region: "us-east-1", Prefix: "owned-test", AccessKey: "test-access", SecretKey: "test-secret", MasterKey: hex.EncodeToString(key)}
	data, _ := json.Marshal(resources.Settings{Backends: []resources.BackendConfig{f.Config}})
	if err = configstore.Write(root, configstore.Storage, data); err != nil {
		t.Fatal(err)
	}
	if err = resources.Prepare(t.Context(), state, f.Config); err != nil {
		t.Fatal(err)
	}
	return f
}
func publish(t *testing.T, lib resources.Library, data []byte) resources.Ref {
	t.Helper()
	release, err := lib.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	hash, size, err := lib.Put(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return resources.Ref{Hash: hash, Bytes: size, MediaType: "image/png", Name: "original.png"}
}
func startMigration(t *testing.T, f *storageFixture, target string) resources.Migration {
	t.Helper()
	control := f.Store.ResourceControl()
	stamp, err := control.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	job, err := control.Start(t.Context(), target, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return *job
}
func copyMissing(t *testing.T, f *storageFixture, job resources.Migration) {
	t.Helper()
	control := f.Store.ResourceControl()
	missing, err := control.Missing(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range missing {
		if err = control.Copy(t.Context(), job, item); err != nil {
			t.Fatal(err)
		}
	}
}
func TestResourceMigrationCopiesNewPublicationsAndFencesLateCommit(t *testing.T) {
	f := newStorageFixture(t)
	ctx := t.Context()
	original, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if _, err := f.Store.Create(ctx, "owner", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Store.BeginRunWithImages(ctx, "owner", "run", "message", "look", []store.Image{{Data: original, MediaType: "image/png", Name: "original.png"}}); err != nil {
		t.Fatal(err)
	}
	refs, err := f.Store.MessageResources(ctx, "owner", "message")
	if err != nil || len(refs) != 1 {
		t.Fatal(err)
	}
	oldStamp, err := f.Library.Stamp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = resources.CheckBackend(ctx, f.Library.StateDir, f.Config); err != nil {
		t.Fatal(err)
	}
	job := startMigration(t, f, f.Config.ID)
	copyMissing(t, f, job)
	extra := publish(t, f.Library, []byte("a model produced this immutable resource during synchronization"))
	if done, err := f.Store.ResourceControl().Cutover(ctx, job); err != nil || done {
		t.Fatalf("cutover missed a new original: done=%v err=%v", done, err)
	}
	copyMissing(t, f, job)
	// A real final check uses only local metadata, not a remote HEAD/list/hash.
	requests := f.S3.Requests.Load()
	if done, err := f.Store.ResourceControl().Cutover(ctx, job); err != nil || !done {
		t.Fatalf("cutover: %v %v", done, err)
	}
	if f.S3.Requests.Load() != requests {
		t.Fatal("cutover performed network I/O")
	}
	if stamp, err := f.Library.Stamp(ctx); err != nil || stamp.BackendID != f.Config.ID || stamp.Epoch != oldStamp.Epoch+1 {
		t.Fatal("active storage/epoch not committed together", err)
	}
	if err = f.Store.AttachResource(ctx, "owner", refs[0], nil, oldStamp); !errors.Is(err, resources.ErrStorageChanged) {
		t.Fatalf("late source publication was not fenced: %v", err)
	}
	if images, err := f.Store.MessageImages(ctx, "owner", "message"); err != nil || len(images) != 1 || !bytes.Equal(images[0].Data, original) {
		t.Fatal("canonical ownership did not survive", err)
	}
	if _, err = f.Library.Read(extra); err != nil {
		t.Fatal(err)
	}
	for _, key := range f.S3.Keys() {
		if bytes.Contains(f.S3.Raw(key), original) {
			t.Fatal("object storage contained plaintext")
		}
	}
	stats, err := f.Store.ResourceStorage(ctx)
	if err != nil || stats.ReferencedCount != 1 || stats.CleanableCount != 3 {
		t.Fatalf("physical duplicate accounting: %+v %v", stats, err)
	}
	count, _, err := f.Store.CleanResources(ctx, nil)
	if err != nil || count != 3 {
		t.Fatalf("retired/orphan cleanup: %d %v", count, err)
	}
	if _, err = f.Store.MessageImages(ctx, "owner", "message"); err != nil {
		t.Fatal("cleanup removed active resource identity", err)
	}
	// Returning to local is a fresh reverse migration, not reactivation of a
	// source snapshot which predates newly generated resources.
	generated := publish(t, f.Library, bytes.Repeat([]byte("new audio/video bytes"), 8000))
	reverse := startMigration(t, f, resources.LocalBackend)
	copyMissing(t, f, reverse)
	if done, err := f.Store.ResourceControl().Cutover(ctx, reverse); err != nil || !done {
		t.Fatal(done, err)
	}
	if data, err := f.Library.Read(generated); err != nil || int64(len(data)) != generated.Bytes {
		t.Fatal("reverse migration lost new output", err)
	}
}
func TestResourceMigrationNetworkCopyDoesNotHoldPublicationOrSQLTransaction(t *testing.T) {
	f := newStorageFixture(t)
	ref := publish(t, f.Library, bytes.Repeat([]byte("stream"), 40000))
	job := startMigration(t, f, f.Config.ID)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	f.S3.SetHook(func(r *http.Request) int {
		if r.Method == http.MethodPut {
			once.Do(func() { close(entered) })
			select {
			case <-unblock:
			case <-r.Context().Done():
			}
		}
		return 0
	})
	done := make(chan error, 1)
	go func() {
		done <- f.Store.ResourceControl().Copy(t.Context(), job, resources.File{Hash: ref.Hash, Bytes: ref.Bytes})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("copy did not reach target upload")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err := f.Library.Lock(ctx)
	if err != nil {
		close(unblock)
		t.Fatal("copy held publication lock", err)
	}
	release()
	if _, err = f.Store.Create(ctx, "parallel", "parallel"); err != nil {
		close(unblock)
		t.Fatal("copy held SQLite transaction", err)
	}
	close(unblock)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestResourceMigrationCancelAndDeleteRetryKeepSourceAndRecoveryRecords(t *testing.T) {
	f := newStorageFixture(t)
	ref := publish(t, f.Library, []byte("source remains authoritative"))
	job := startMigration(t, f, f.Config.ID)
	copyMissing(t, f, job)
	if err := f.Store.ResourceControl().Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if done, err := f.Store.ResourceControl().Cutover(t.Context(), job); done || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost cutover race: %v %v", done, err)
	}
	if err := f.Store.ResourceControl().FinishCancellation(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if stamp, _ := f.Library.Stamp(t.Context()); stamp.BackendID != resources.LocalBackend {
		t.Fatal("cancel switched active backend")
	}
	f.S3.SetHook(func(r *http.Request) int {
		if r.Method == http.MethodDelete || r.Method == http.MethodPost && r.URL.Query().Has("delete") {
			return http.StatusForbidden
		}
		return 0
	})
	if _, _, err := f.Store.CleanResources(t.Context(), nil); err == nil {
		t.Fatal("failed deletion was reported as reclaimed")
	}
	f.S3.SetHook(nil)
	if _, _, err := f.Store.CleanResources(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.S3.Count() != 0 {
		t.Fatal("cancelled target objects were not reclaimed")
	}
	// No session owns ref; ordinary unreferenced cleanup may remove its source.
	if _, err := f.Library.Read(ref); err == nil {
		t.Fatal("unreferenced source was not reclaimed")
	}
}
func TestResourceMigrationRestartReverifiesTargetAndRetainsLogicalIdentity(t *testing.T) {
	f := newStorageFixture(t)
	ref := publish(t, f.Library, []byte("restart proof"))
	job := startMigration(t, f, f.Config.ID)
	copyMissing(t, f, job)
	if err := f.Store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.Store, err = store.Open(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := f.Store.ResourceControl().Migration(t.Context())
	if err != nil || restored == nil || restored.ID != job.ID || restored.Phase != "syncing" {
		t.Fatal("migration state lost on restart", err)
	}
	if err = f.Store.ResourceControl().Reverify(t.Context(), *restored); err != nil {
		t.Fatal(err)
	}
	keys := f.S3.Keys()
	if len(keys) != 1 {
		t.Fatal("unexpected object set")
	}
	f.S3.Corrupt(keys[0])
	if err = f.Store.ResourceControl().Reverify(t.Context(), *restored); err != nil {
		t.Fatal(err)
	}
	if done, err := f.Store.ResourceControl().Cutover(t.Context(), *restored); err != nil || done {
		t.Fatal("corrupted target proof survived", done, err)
	}
	if data, err := f.Library.Read(ref); err != nil || string(data) != "restart proof" {
		t.Fatal("source no longer available after target failure", err)
	}
}
func TestResourceMigrationConfigurationAndScopeValidation(t *testing.T) {
	f := newStorageFixture(t)
	for _, prefix := range []string{"../other", "a/../b", "/root", "a\\b"} {
		cfg := f.Config
		cfg.Prefix = prefix
		if cfg.Validate() == nil {
			t.Fatal("ambiguous storage scope accepted")
		}
	}
	cfg := f.Config
	cfg.Endpoint = "https://user:secret@example.test"
	if cfg.Validate() == nil {
		t.Fatal("credential-bearing endpoint accepted")
	}
	raw, _ := json.Marshal(f.Config.Public())
	for _, secret := range []string{f.Config.AccessKey, f.Config.SecretKey, f.Config.MasterKey} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("public storage view leaked credentials")
		}
	}
}

func TestResourceCleanupFindsUnjournaledOwnedObjectsWithoutTouchingOtherNamespaces(t *testing.T) {
	f := newStorageFixture(t)
	objectRoot, _ := dofs.ObjectRoot(f.Config.ID)
	owned := f.Config.Prefix + "/" + objectRoot + "checks/late-arrival"
	foreign := f.Config.Prefix + "/.dofs/v1/namespaces/another-owner/objects/retain"
	f.S3.PutUntracked(owned, []byte("uncertain late PUT"))
	f.S3.PutUntracked(foreign, []byte("must remain"))
	stats, err := f.Store.ResourceStorage(t.Context())
	if err != nil || stats.CleanableCount != 1 {
		t.Fatalf("untracked owned object was not discovered: %+v %v", stats, err)
	}
	if count, _, err := f.Store.CleanResources(t.Context(), nil); err != nil || count != 1 {
		t.Fatal("untracked object cleanup failed", count, err)
	}
	if f.S3.Count() != 1 || string(f.S3.Raw(foreign)) != "must remain" {
		t.Fatal("cleanup crossed namespace boundary")
	}
}

func TestResourceMigrationRetriesAnInterruptedSingleFileWithoutLosingRecoveryKeys(t *testing.T) {
	f := newStorageFixture(t)
	ref := publish(t, f.Library, []byte("retry interrupted immutable upload"))
	job := startMigration(t, f, f.Config.ID)
	f.S3.SetHook(func(r *http.Request) int {
		if r.Method == http.MethodPut {
			return 403
		}
		return 0
	})
	if err := f.Store.ResourceControl().Copy(t.Context(), job, resources.File{Hash: ref.Hash, Bytes: ref.Bytes}); err == nil {
		t.Fatal("failed upload was accepted")
	}
	f.S3.SetHook(nil)
	copyMissing(t, f, job)
	if done, err := f.Store.ResourceControl().Cutover(t.Context(), job); err != nil || !done {
		t.Fatal("retry failed to reach cutover", done, err)
	}
	if data, err := f.Library.Read(ref); err != nil || string(data) != "retry interrupted immutable upload" {
		t.Fatal("retried original changed", err)
	}
}

func TestResourceMigrationDoesNotForgetMissingRetainedOriginals(t *testing.T) {
	f := newStorageFixture(t)
	ref := publish(t, f.Library, []byte("retained original"))
	if _, err := f.Store.Create(t.Context(), "owner", "owner"); err != nil {
		t.Fatal(err)
	}
	stamp, err := f.Library.Stamp(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Store.AttachResource(t.Context(), "owner", ref, nil, stamp); err != nil {
		t.Fatal(err)
	}
	path, err := f.Library.Path(ref.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	job := startMigration(t, f, f.Config.ID)
	missing, err := f.Store.ResourceControl().Missing(t.Context(), job)
	if err != nil || len(missing) != 1 || missing[0].Hash != ref.Hash {
		t.Fatal("missing retained resource vanished from migration inventory", err)
	}
	if done, err := f.Store.ResourceControl().Cutover(t.Context(), job); err != nil || done {
		t.Fatal("cutover ignored a missing retained original", done, err)
	}
	if err = f.Store.ResourceControl().Copy(t.Context(), job, missing[0]); err == nil {
		t.Fatal("missing original was reported as copied")
	}
}
