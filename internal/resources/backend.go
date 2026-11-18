package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/willvar/dofs"
	dofssqlite "github.com/willvar/dofs/metadata/sqlite"
	dofss3 "github.com/willvar/dofs/object/s3"
	"net/url"
	"vista/internal/network"
)

// Backend owns its readers, metadata connection and key-provider state until
// Close. Logical resource identity is independent of the physical locator.
type Backend interface {
	Open(context.Context, string) (io.ReadCloser, int64, string, error)
	PutFile(context.Context, *os.File, string, int64) error
	Files(context.Context, bool) ([]File, error)
	Remove(context.Context, File) error
	Close() error
}

type localBackend struct{ library Library }

func (b *localBackend) Open(ctx context.Context, hash string) (io.ReadCloser, int64, string, error) {
	path, err := b.library.Path(hash)
	if err != nil {
		return nil, 0, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, "", err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, "", errors.New("resource is not a regular file")
	}
	return f, info.Size(), hash, nil
}
func (b *localBackend) PutFile(ctx context.Context, f *os.File, hash string, size int64) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	actual, n, err := b.library.putLocal(ctx, f)
	if err == nil && (actual != hash || n != size) {
		return errors.New("resource changed while publishing")
	}
	return err
}
func (b *localBackend) Files(ctx context.Context, includePending bool) ([]File, error) {
	var out []File
	err := b.library.localFiles(ctx, func(f File) error {
		f.BackendID = LocalBackend
		f.ID = f.path
		if includePending || f.Hash != "" {
			out = append(out, f)
		}
		return nil
	})
	return out, err
}
func (b *localBackend) Remove(ctx context.Context, f File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(f.path)
}
func (b *localBackend) Close() error { return nil }

type objectIntent struct {
	UploadID  string `json:"uploadId"`
	ObjectKey string `json:"objectKey"`
	Hash      string `json:"hash"`
	Bytes     int64  `json:"bytes"`
}
type objectBackend struct {
	config    BackendConfig
	stateDir  string
	meta      *dofssqlite.Store
	objects   dofs.ObjectStore
	keys      *dofs.EnvelopeKeyProvider
	backend   *dofs.Backend
	client    *minio.Client
	transport network.Transport
}

func (l Library) ConfigDir() string { return filepath.Dir(l.StateDir) }
func (l Library) OpenBackend(ctx context.Context, id string) (Backend, error) {
	if id == LocalBackend {
		return &localBackend{library: l}, nil
	}
	settings, err := LoadSettings(l.ConfigDir())
	if err != nil {
		return nil, err
	}
	cfg, err := settings.Backend(id)
	if err != nil {
		return nil, err
	}
	return openObjectBackend(ctx, l.StateDir, cfg, false)
}

func openObjectBackend(ctx context.Context, stateDir string, cfg BackendConfig, prepare bool) (_ *objectBackend, err error) {
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	u, _ := url.Parse(cfg.Endpoint)
	base, err := minio.DefaultTransport(u.Scheme == "https")
	if err != nil {
		return nil, err
	}
	settings, err := network.Load(filepath.Dir(stateDir))
	if err != nil {
		return nil, err
	}
	policy, err := settings.Select(settings.GlobalProxy)
	if err != nil {
		return nil, err
	}
	transport := network.Transport{Base: base, Policy: &policy, Scope: network.ApplicationScope}

	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken), Secure: u.Scheme == "https", Region: cfg.Region, Transport: transport})
	if err != nil {
		return nil, errors.New("could not create object storage client")
	}
	objects, err := dofss3.NewWithClient(client, cfg.Bucket, cfg.Prefix)
	if err != nil {
		return nil, err
	}
	meta, err := dofssqlite.Open(ctx, dofssqlite.Config{Path: metadataPath(stateDir, cfg.ID)})
	if err != nil {
		return nil, err
	}
	b := &objectBackend{config: cfg, stateDir: stateDir, meta: meta, objects: objects, client: client, transport: transport}
	defer func() {
		if err != nil {
			b.Close()
		}
	}()
	if prepare {
		if err = meta.Migrate(ctx); err != nil {
			return nil, err
		}
	}
	master, _ := hex.DecodeString(cfg.MasterKey)
	b.keys, err = dofs.NewEnvelopeKeyProvider(master)
	dofs.Clear(master)
	if err != nil {
		return nil, err
	}
	if prepare {
		if _, err = meta.GetNamespace(ctx, cfg.ID); errors.Is(err, dofs.ErrNotFound) {
			var wrapped []byte
			wrapped, err = b.keys.WrapNewNamespaceKey()
			if err != nil {
				return nil, err
			}
			_, err = meta.CreateNamespace(ctx, dofs.Namespace{ID: cfg.ID, Label: "Vista resources", WrappedKEK: wrapped, HistoryLimit: 1})
		}
		if err != nil {
			return nil, err
		}
	}
	b.backend, err = dofs.NewBackend(ctx, cfg.ID, meta, objects, b.keys, dofs.BackendOptions{ControlPlane: true})
	if err != nil {
		return nil, err
	}
	return b, nil
}
func (b *objectBackend) Close() error {
	b.transport.CloseIdleConnections()
	var err error
	if b.backend != nil {
		err = b.backend.Close()
	}
	if b.keys != nil {
		b.keys.Close()
	}
	if b.meta != nil {
		err = errors.Join(err, b.meta.Close())
	}
	return err
}
func (b *objectBackend) Open(ctx context.Context, hash string) (io.ReadCloser, int64, string, error) {
	if !ValidHash(hash) {
		return nil, 0, "", errors.New("invalid resource hash")
	}
	n, err := b.backend.Lookup(ctx, dofs.RootInode, hash)
	if err != nil {
		return nil, 0, "", err
	}
	r, err := b.backend.OpenPlaintext(ctx, n)
	return r, n.Size, fmt.Sprintf("%d:%d", n.Inode, n.Generation), err
}
func (b *objectBackend) intentPath(id string) string {
	return filepath.Join(b.stateDir, "resource-backends", b.config.ID, "uploads", id+".json")
}
func (b *objectBackend) PutFile(ctx context.Context, f *os.File, hash string, size int64) error {
	if !ValidHash(hash) || size < 0 {
		return errors.New("invalid resource")
	}
	if n, err := b.backend.Lookup(ctx, dofs.RootInode, hash); err == nil {
		if n.Size != size {
			return errors.New("existing resource size mismatch")
		}
		r, _, _, err := b.Open(ctx, hash)
		if err != nil {
			return err
		}
		defer r.Close()
		return verifyReader(ctx, r, hash, size)
	} else if !errors.Is(err, dofs.ErrNotFound) {
		return err
	}
	// A failed single-file transfer is retried as a fresh immutable upload.
	// Retain its exact recovery key if remote deletion has an uncertain result.
	uploads, err := b.meta.ListExternalUploads(ctx, b.config.ID, dofs.ExternalUploadActive)
	if err != nil {
		return err
	}
	for _, upload := range uploads {
		node, err := b.meta.GetNode(ctx, b.config.ID, upload.Inode)
		if err != nil {
			return err
		}
		if node.Name != hash {
			continue
		}
		if err = saveIntentIfMissing(b.intentPath(upload.ID), objectIntent{UploadID: upload.ID, ObjectKey: upload.ObjectKey, Hash: hash, Bytes: upload.Size}); err != nil {
			return err
		}
		if err = b.abortMultipart(ctx, upload.ObjectKey); err != nil {
			return err
		}
		abortErr := b.backend.AbortDirectUpload(ctx, upload.ID)
		if _, err = b.meta.GetExternalUpload(ctx, b.config.ID, upload.ID); !errors.Is(err, dofs.ErrNotFound) {
			if abortErr != nil {
				return abortErr
			}
			if err != nil {
				return err
			}
			return errors.New("incomplete resource reservation is still active")
		}
		if abortErr == nil {
			if err = removeIntent(b.intentPath(upload.ID)); err != nil {
				return err
			}
		}
	}
	// Intent precedes all payload transfer and survives AbortDirectUpload's
	// metadata-first deletion, so a failed remote deletion never loses its key.
	key, err := dofs.GenerateKey()
	if err != nil {
		return err
	}
	defer dofs.Clear(key)
	up, err := b.backend.PrepareDirectUpload(ctx, dofs.DirectUploadRequest{ID: uuid.NewString(), Parent: dofs.RootInode, Name: hash, Mode: 0600, Size: size, FileKey: key})
	if err != nil {
		return err
	}
	intent := objectIntent{UploadID: up.ID, ObjectKey: up.ObjectKey, Hash: hash, Bytes: size}
	if err = saveIntent(b.intentPath(up.ID), intent); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	transferCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		e := dofs.EncryptStream(key, &contextReader{ctx: transferCtx, reader: f}, writer)
		_ = writer.CloseWithError(e)
		done <- e
	}()
	encryptedSize, err := dofs.CheckedEncryptedSize(size, dofs.DefaultChunkSize)
	if err == nil {
		err = b.objects.Put(transferCtx, up.ObjectKey, reader, encryptedSize)
	}
	if err != nil {
		cancel()
		_ = reader.CloseWithError(err)
	}
	streamErr := <-done
	_ = reader.Close()
	if err != nil {
		return err
	}
	if streamErr != nil {
		return streamErr
	}
	if _, err = b.backend.CommitDirectUpload(ctx, up.ID); err != nil {
		return err
	}
	// Durable ready-node metadata precedes acknowledgement and intent removal.
	if err = b.backend.AcknowledgeDirectUpload(ctx, up.ID); err != nil {
		return err
	}
	return removeIntent(b.intentPath(up.ID))
}
func saveIntent(path string, value objectIntent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".intent-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncResourceDirectory(filepath.Dir(path))
}
func removeIntent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (b *objectBackend) Files(ctx context.Context, includePending bool) ([]File, error) {
	var out []File
	nodes, err := b.meta.ListState(ctx, b.config.ID, dofs.NodeStateReady)
	if err != nil {
		return nil, err
	}
	ready := map[string]bool{}
	for _, n := range nodes {
		if n.IsFile() && ValidHash(n.Name) {
			ready[n.ObjectKey] = true
			out = append(out, File{Hash: n.Name, Bytes: n.Size, BackendID: b.config.ID, ID: fmt.Sprintf("node:%d:%d", n.Inode, n.Generation), ObjectKey: n.ObjectKey})
		}
	}
	if !includePending {
		return out, ctx.Err()
	}
	// Tombstones remain visible as unreferenced until all generations are gone.
	deleted, err := b.meta.ListState(ctx, b.config.ID, dofs.NodeStateDeleted)
	if err != nil {
		return nil, err
	}
	for _, n := range deleted {
		if n.IsFile() {
			out = append(out, File{Hash: n.Name, Bytes: n.Size, BackendID: b.config.ID, ID: fmt.Sprintf("deleted:%d", n.Inode), ObjectKey: n.ObjectKey, Pending: true})
		}
	}
	uploads, err := b.meta.ListExternalUploads(ctx, b.config.ID, dofs.ExternalUploadActive)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, u := range uploads {
		known[u.ID] = true
		out = append(out, File{Bytes: u.Size, BackendID: b.config.ID, ID: "upload:" + u.ID, ObjectKey: u.ObjectKey, Pending: true})
	}
	entries, err := os.ReadDir(filepath.Join(b.stateDir, "resource-backends", b.config.ID, "uploads"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, e := os.ReadFile(b.intentPath(strings.TrimSuffix(entry.Name(), ".json")))
		if e != nil {
			return nil, e
		}
		var intent objectIntent
		if json.Unmarshal(data, &intent) != nil {
			return nil, errors.New("invalid resource upload recovery record")
		}
		root, _ := dofs.ObjectRoot(b.config.ID)
		if intent.UploadID != strings.TrimSuffix(entry.Name(), ".json") || !strings.HasPrefix(intent.ObjectKey, root) || strings.Contains(intent.ObjectKey, "..") || intent.Bytes < 0 {
			return nil, errors.New("invalid resource upload recovery scope")
		}
		if !known[intent.UploadID] && !ready[intent.ObjectKey] {
			out = append(out, File{Bytes: intent.Bytes, BackendID: b.config.ID, ID: "intent:" + intent.UploadID, ObjectKey: intent.ObjectKey, Pending: true})
		}
	}
	prefix := b.config.Prefix
	if prefix != "" {
		prefix += "/"
	}
	root, _ := dofs.ObjectRoot(b.config.ID)
	prefix += root
	for upload := range b.client.ListIncompleteUploads(ctx, b.config.Bucket, prefix, true) {
		if upload.Err != nil {
			return nil, upload.Err
		}
		if !strings.HasPrefix(upload.Key, prefix) {
			return nil, errors.New("incomplete-upload inventory exceeded the resource namespace")
		}
		key := strings.TrimPrefix(upload.Key, strings.TrimSuffix(b.config.Prefix, "/")+"/")
		if b.config.Prefix == "" {
			key = upload.Key
		}
		out = append(out, File{Bytes: upload.Size, BackendID: b.config.ID, ID: "multipart:" + upload.Key, ObjectKey: key, Pending: true})
	}
	// Reconcile the exact namespace as well as durable metadata. This catches
	// an uncertain PUT which becomes visible after a cancelled request or a
	// completed cleanup pass. Never inventory the bucket or another namespace.
	referenced := map[string]bool{}
	for _, file := range out {
		if file.ObjectKey != "" {
			referenced[file.ObjectKey] = true
		}
	}
	for _, node := range append(nodes, deleted...) {
		if !node.IsFile() {
			continue
		}
		generations, err := b.meta.ListGenerations(ctx, b.config.ID, node.Inode)
		if err != nil {
			return nil, err
		}
		for _, generation := range generations {
			referenced[generation.ObjectKey] = true
		}
	}
	for object := range b.client.ListObjects(ctx, b.config.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			return nil, object.Err
		}
		if !strings.HasPrefix(object.Key, prefix) {
			return nil, errors.New("object inventory exceeded the resource namespace")
		}
		key := object.Key
		if b.config.Prefix != "" {
			key = strings.TrimPrefix(key, b.config.Prefix+"/")
		}
		if !referenced[key] {
			out = append(out, File{Bytes: object.Size, BackendID: b.config.ID, ID: "object:" + key, ObjectKey: key, Pending: true})
		}
	}
	return out, ctx.Err()
}
func (b *objectBackend) Remove(ctx context.Context, f File) error {
	switch {
	case strings.HasPrefix(f.ID, "node:"):
		n, err := b.backend.Lookup(ctx, dofs.RootInode, f.Hash)
		if errors.Is(err, dofs.ErrNotFound) {
			break
		}
		if err != nil {
			return err
		}
		if f.ID != fmt.Sprintf("node:%d:%d", n.Inode, n.Generation) {
			return errors.New("resource generation changed")
		}
		if err = b.backend.Remove(ctx, dofs.RootInode, f.Hash, false); err != nil {
			return err
		}
	case strings.HasPrefix(f.ID, "upload:"):
		id := strings.TrimPrefix(f.ID, "upload:")
		if err := saveIntentIfMissing(b.intentPath(id), objectIntent{UploadID: id, ObjectKey: f.ObjectKey, Bytes: f.Bytes}); err != nil {
			return err
		}
		if err := b.abortMultipart(ctx, f.ObjectKey); err != nil {
			return err
		}
		if err := b.backend.AbortDirectUpload(ctx, id); err != nil && !errors.Is(err, dofs.ErrNotFound) {
			return err
		}
	case strings.HasPrefix(f.ID, "intent:"), strings.HasPrefix(f.ID, "object:"):
		// Publication/deletion protection is held by the caller. Never delete a
		// key which has since become a ready generation.
		nodes, err := b.meta.ListState(ctx, b.config.ID, dofs.NodeStateReady)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if n.ObjectKey == f.ObjectKey {
				return errors.New("resource upload is now published")
			}
		}
		root, _ := dofs.ObjectRoot(b.config.ID)
		if !strings.HasPrefix(f.ObjectKey, root) {
			return errors.New("invalid cleanup object scope")
		}
		for _, node := range nodes {
			if !node.IsFile() {
				continue
			}
			generations, err := b.meta.ListGenerations(ctx, b.config.ID, node.Inode)
			if err != nil {
				return err
			}
			for _, generation := range generations {
				if generation.ObjectKey == f.ObjectKey {
					return errors.New("resource generation is still retained")
				}
			}
		}
		uploads, err := b.meta.ListExternalUploads(ctx, b.config.ID, dofs.ExternalUploadActive)
		if err != nil {
			return err
		}
		for _, upload := range uploads {
			if upload.ObjectKey == f.ObjectKey {
				return errors.New("resource upload reservation is still active")
			}
		}

		if err = b.abortMultipart(ctx, f.ObjectKey); err != nil {
			return err
		}
		if err = b.objects.Delete(ctx, f.ObjectKey); err != nil && !errors.Is(err, dofs.ErrObjectNotFound) {
			return err
		}
	case strings.HasPrefix(f.ID, "multipart:"):
		root, _ := dofs.ObjectRoot(b.config.ID)
		prefix := root
		if b.config.Prefix != "" {
			prefix = b.config.Prefix + "/" + prefix
		}
		key := strings.TrimPrefix(f.ID, "multipart:")
		if !strings.HasPrefix(key, prefix) {
			return errors.New("invalid multipart cleanup scope")
		}
		return b.abortMultipart(ctx, strings.TrimPrefix(key, strings.TrimSuffix(b.config.Prefix, "/")+"/"))
	case strings.HasPrefix(f.ID, "deleted:"):
		if _, err := strconv.ParseUint(strings.TrimPrefix(f.ID, "deleted:"), 10, 64); err != nil {
			return err
		}
	default:
		return errors.New("invalid resource cleanup identity")
	}
	if _, err := dofs.ReclaimDeleted(ctx, b.meta, b.objects, b.config.ID); err != nil {
		return err
	}
	if f.ObjectKey != "" {
		if _, err := b.objects.Stat(ctx, f.ObjectKey); err == nil {
			return errors.New("resource deletion is pending")
		} else if !errors.Is(err, dofs.ErrObjectNotFound) {
			return err
		}
	}
	if strings.HasPrefix(f.ID, "upload:") || strings.HasPrefix(f.ID, "intent:") {
		return removeIntent(b.intentPath(strings.SplitN(f.ID, ":", 2)[1]))
	}
	return nil
}
func saveIntentIfMissing(path string, value objectIntent) error {
	err := saveIntent(path, value)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func verifyReader(ctx context.Context, r io.Reader, hash string, size int64) error {
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(&contextReader{ctx: ctx, reader: r}, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(sum.Sum(nil)) != hash {
		return errors.New("resource content does not match its hash")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Some S3 services deliberately do not implement prefix-based multipart
// inventory. Every managed transfer has a durable exact key, so use the SDK's
// exact-object list/abort path as well; never fall back to bucket-wide scans.
func (b *objectBackend) abortMultipart(ctx context.Context, key string) error {
	root, _ := dofs.ObjectRoot(b.config.ID)
	if !strings.HasPrefix(key, root) {
		return errors.New("invalid incomplete-upload scope")
	}
	physical := key
	if b.config.Prefix != "" {
		physical = b.config.Prefix + "/" + key
	}
	if err := b.client.RemoveIncompleteUpload(ctx, b.config.Bucket, physical); err != nil {
		return err
	}
	page, err := (minio.Core{Client: b.client}).ListMultipartUploads(ctx, b.config.Bucket, physical, "", "", "", 1000)
	if err != nil {
		return err
	}
	for _, upload := range page.Uploads {
		if upload.Key == physical {
			return errors.New("incomplete upload deletion is pending")
		}
	}
	return nil
}
