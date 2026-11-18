package resources_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/willvar/dofs"
	dofssqlite "github.com/willvar/dofs/metadata/sqlite"
	dofss3 "github.com/willvar/dofs/object/s3"
	"vista/internal/configstore"
	"vista/internal/platform"
	"vista/internal/resources"
	"vista/internal/store"
)

// This is a real S3 service contract, not the HTTP fixture. Only a newly
// generated child prefix/namespace is written or deleted; no bucket policy,
// account identity, pre-existing resource or configured application is changed.
func TestResourceStorageLiveS3(t *testing.T) {
	endpoint := os.Getenv("VISTA_TEST_S3_ENDPOINT")
	if endpoint == "" && os.Getenv("VISTA_TEST_S3_BINARY") != "" {
		endpoint = startLiveS3(t, os.Getenv("VISTA_TEST_S3_BINARY"))
	}
	if endpoint == "" {
		t.Skip("set VISTA_TEST_S3_BINARY or explicit S3 endpoint/credentials for the real storage contract")
	}

	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
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
	database, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := dofs.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer dofs.Clear(key)
	prefix := strings.Trim(os.Getenv("VISTA_TEST_S3_PREFIX"), "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += "vista-validation/" + uuid.NewString()
	cfg := resources.BackendConfig{ID: uuid.NewString(), Kind: "s3", Endpoint: endpoint, Bucket: os.Getenv("VISTA_TEST_S3_BUCKET"), Region: os.Getenv("VISTA_TEST_S3_REGION"), Prefix: prefix, AccessKey: os.Getenv("VISTA_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("VISTA_TEST_S3_SECRET_KEY"), SessionToken: os.Getenv("VISTA_TEST_S3_SESSION_TOKEN"), MasterKey: hex.EncodeToString(key)}
	if err = cfg.Validate(); err != nil {
		t.Fatal("explicit storage test configuration is invalid")
	}
	raw, _ := json.Marshal(resources.Settings{Backends: []resources.BackendConfig{cfg}})
	if err = configstore.Write(root, configstore.Storage, raw); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(endpoint)
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken), Secure: u.Scheme == "https", Region: cfg.Region})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := dofss3.NewWithClient(client, cfg.Bucket, cfg.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup is installed before the first remote payload. It touches this
	// exact test prefix only, including incomplete multipart uploads on failure.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		for item := range client.ListIncompleteUploads(ctx, cfg.Bucket, cfg.Prefix+"/", true) {
			if item.Err != nil {
				t.Error("could not verify incomplete test uploads")
				return
			}
			if err := client.RemoveIncompleteUpload(ctx, cfg.Bucket, item.Key); err != nil {
				t.Error("could not remove incomplete test upload")
				return
			}
		}
		metadata, err := dofssqlite.Open(ctx, dofssqlite.Config{Path: filepath.Join(state, "resource-backends", cfg.ID, "metadata.sqlite")})
		if err == nil {
			defer metadata.Close()
			_ = dofs.DeleteNamespace(ctx, metadata, objects, cfg.ID)
		}
		// Recovery also covers a check object's metadata-free encrypted payload.
		objectRoot, _ := dofs.ObjectRoot(cfg.ID)
		if err = objects.DeletePrefix(ctx, objectRoot); err != nil {
			t.Error("could not delete test namespace objects")
			return
		}
		for item := range client.ListObjects(ctx, cfg.Bucket, minio.ListObjectsOptions{Prefix: cfg.Prefix + "/", Recursive: true}) {
			if item.Err != nil {
				t.Error("could not confirm test cleanup")
				return
			}
			t.Error("test object remains after cleanup")
			return
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err = resources.Prepare(ctx, state, cfg); err != nil {
		t.Fatal("real storage metadata preparation failed", err)
	}
	if err = resources.CheckBackend(ctx, state, cfg); err != nil {
		t.Fatal("real storage encrypted read/write/delete check failed", err)
	}
	lib := resources.Library{StateDir: state}
	original := publish(t, lib, bytes.Repeat([]byte("resource migration live contract\n"), 3000))
	control := database.ResourceControl()
	stamp, err := control.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := control.Start(ctx, cfg.ID, stamp)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := control.Missing(ctx, *job)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range missing {
		if err = control.Copy(ctx, *job, item); err != nil {
			t.Fatal("real source copy failed", err)
		}
	}
	if ok, err := control.Cutover(ctx, *job); err != nil || !ok {
		t.Fatal("real cutover failed", err)
	}
	if data, err := lib.ReadContext(ctx, original); err != nil || int64(len(data)) != original.Bytes {
		t.Fatal("real migrated read failed", err)
	}
	// An output generated after cutover uses the selected object backend.
	produced := publish(t, lib, []byte("new immutable model output after cutover"))
	backend, err := lib.OpenBackend(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, _, err := backend.Open(ctx, produced.Hash)
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	backend.Close()
	if err != nil || string(data) != "new immutable model output after cutover" {
		t.Fatal("real produced resource did not round-trip", err)
	}
	// Simulate an interrupted managed upload: DOFS reserves its exact key
	// before payload transfer. This remains discoverable on services which do
	// not implement namespace-prefix multipart listing.
	metadata, err := dofssqlite.Open(ctx, dofssqlite.Config{Path: filepath.Join(state, "resource-backends", cfg.ID, "metadata.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	keyProvider, err := dofs.NewEnvelopeKeyProvider(key)
	if err != nil {
		metadata.Close()
		t.Fatal(err)
	}
	controller, err := dofs.NewBackend(ctx, cfg.ID, metadata, objects, keyProvider, dofs.BackendOptions{ControlPlane: true})
	if err != nil {
		keyProvider.Close()
		metadata.Close()
		t.Fatal(err)
	}
	partialKey, err := dofs.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	partialHash := sha256.Sum256([]byte("incomplete managed resource"))
	reservation, err := controller.PrepareDirectUpload(ctx, dofs.DirectUploadRequest{Parent: dofs.RootInode, Name: hex.EncodeToString(partialHash[:]), Size: 16384, FileKey: partialKey})
	dofs.Clear(partialKey)
	controller.Close()
	keyProvider.Close()
	metadata.Close()
	if err != nil {
		t.Fatal(err)
	}
	multipartKey := cfg.Prefix + "/" + reservation.ObjectKey
	uploadID, err := (minio.Core{Client: client}).NewMultipartUpload(ctx, cfg.Bucket, multipartKey, minio.PutObjectOptions{})
	if err != nil || uploadID == "" {
		t.Fatal("real incomplete-upload preparation failed", err)
	}
	part := bytes.Repeat([]byte("incomplete"), 1024)
	if _, err = (minio.Core{Client: client}).PutObjectPart(ctx, cfg.Bucket, multipartKey, uploadID, 1, bytes.NewReader(part), int64(len(part)), minio.PutObjectPartOptions{}); err != nil {
		t.Fatal("real incomplete part upload failed", err)
	}
	pendingFiles, err := lib.BackendFiles(ctx, cfg.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range pendingFiles {
		if file.ObjectKey == reservation.ObjectKey {
			found = true
			if err = lib.RemoveFile(ctx, file); err != nil {
				t.Fatal("real incomplete upload reclamation failed", err)
			}
		}
	}
	if !found {
		t.Fatal("managed incomplete upload was not detected")
	}
	remaining, err := (minio.Core{Client: client}).ListMultipartUploads(ctx, cfg.Bucket, multipartKey, "", "", "", 1000)
	if err != nil || len(remaining.Uploads) != 0 {
		t.Fatal("real incomplete upload was not physically aborted", err)
	}
	files, err := lib.BackendFiles(ctx, cfg.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err = lib.RemoveFile(ctx, file); err != nil {
			t.Fatal("real physical reclamation failed", err)
		}
	}
	if files, err = lib.BackendFiles(ctx, cfg.ID, false); err != nil || len(files) != 0 {
		t.Fatal("real namespace was not empty after reclamation", err)
	}
}

// A native test-only server runs as this test's child on loopback, without a
// container, system service, global configuration or inherited login state.
func startLiveS3(t *testing.T, binary string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	key := "vista-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	secret := strings.ReplaceAll(uuid.NewString(), "-", "")
	command := exec.Command(binary, "server", t.TempDir(), "--address", address, "--console-address", "127.0.0.1:0", "--quiet")
	command.Env = append(os.Environ(), "MINIO_ROOT_USER="+key, "MINIO_ROOT_PASSWORD="+secret, "MINIO_BROWSER=off", "MINIO_UPDATE=off")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	platform.AttachToParent(command)
	if err = command.Start(); err != nil {
		t.Fatal("could not start isolated native S3 server", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	endpoint := "http://" + address
	client := http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint + "/minio/health/ready")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		select {
		case <-t.Context().Done():
			t.Fatal("native S3 startup cancelled")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !ready {
		t.Fatal("isolated native S3 server did not become ready")
	}
	sdk, err := minio.New(address, &minio.Options{Creds: credentials.NewStaticV4(key, secret, ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	bucket := "vista-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = sdk.MakeBucket(t.Context(), bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		t.Fatal("could not create isolated test bucket", err)
	}
	t.Setenv("VISTA_TEST_S3_ACCESS_KEY", key)
	t.Setenv("VISTA_TEST_S3_SECRET_KEY", secret)
	t.Setenv("VISTA_TEST_S3_BUCKET", bucket)
	t.Setenv("VISTA_TEST_S3_REGION", "us-east-1")
	return endpoint
}
