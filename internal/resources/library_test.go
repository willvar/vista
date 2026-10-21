package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLibraryPreservesOriginalAndDetectsTampering(t *testing.T) {
	library := Library{StateDir: t.TempDir()}
	ctx := context.Background()
	unlock, err := library.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	original := []byte("original bytes, unchanged")
	hash, size, err := library.Put(ctx, bytes.NewReader(original))
	if err != nil || hash != fmt.Sprintf("%x", sha256.Sum256(original)) || size != int64(len(original)) {
		t.Fatalf("original publication: %s %d %v", hash, size, err)
	}
	ref := Ref{Hash: hash, Bytes: size}
	if data, err := library.Read(ref); err != nil || !bytes.Equal(data, original) {
		t.Fatalf("original changed: %q %v", data, err)
	}
	path, _ := library.Path(hash)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(original)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Read(ref); err == nil {
		t.Fatal("same-size corruption was accepted")
	}
	if _, err := library.Path("../" + strings.Repeat("a", 64)); err == nil {
		t.Fatal("invalid hash accepted")
	}
}

func TestResourceLockHonorsCancellation(t *testing.T) {
	library := Library{StateDir: t.TempDir()}
	unlock, err := library.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := library.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock did not honor cancellation: %v", err)
	}
}
