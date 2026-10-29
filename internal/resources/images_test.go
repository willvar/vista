package resources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestImportImagePreservesFiftyMiBOriginalAndRejectsOversize(t *testing.T) {
	ctx := context.Background()
	library := Library{StateDir: t.TempDir()}
	source := filepath.Join(t.TempDir(), "original.png")
	header, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(header); err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(MaxImageBytes); err != nil {
		t.Fatal(err)
	}
	file.Close()
	unlock, err := library.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ref, err := library.ImportImage(ctx, source, "", "image/png")
	if err != nil || ref.Bytes != MaxImageBytes {
		t.Fatalf("50 MiB rejected: %+v %v", ref, err)
	}
	expected := sha256.New()
	original, _ := os.Open(source)
	io.Copy(expected, original)
	original.Close()
	if ref.Hash != fmt.Sprintf("%x", expected.Sum(nil)) {
		t.Fatal("original bytes changed")
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	saved, err := library.Open(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	got := make([]byte, len(header))
	io.ReadFull(saved, got)
	if !bytes.Equal(got, header) {
		t.Fatal("import depends on source")
	}
	oversized, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	oversized.Write(header)
	oversized.Truncate(MaxImageBytes + 1)
	oversized.Close()
	if _, err = library.ImportImage(ctx, source, "", ""); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err = library.ImportImage(ctx, "relative.png", "", ""); err == nil {
		t.Fatal("relative path accepted")
	}
}
