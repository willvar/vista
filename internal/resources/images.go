package resources

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const MaxImageBytes = 50 << 20

func ValidImageType(mediaType string) bool {
	return mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/webp" || mediaType == "image/gif"
}

// ImportImage preserves original bytes. The caller holds the library lock
// through the ownership commit, so explicit cleanup cannot race publication.
func (l Library) ImportImage(ctx context.Context, path, name, expectedType string) (Ref, error) {
	if !filepath.IsAbs(path) {
		return Ref{}, errors.New("an absolute image path is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return Ref{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Ref{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxImageBytes {
		return Ref{}, errors.New("image must be a regular file of 1 byte to 50 MiB")
	}
	prefix := make([]byte, 512)
	n, err := f.Read(prefix)
	if err != nil && err != io.EOF {
		return Ref{}, err
	}
	mediaType := http.DetectContentType(prefix[:n])
	if !ValidImageType(mediaType) {
		return Ref{}, errors.New("unsupported image media type")
	}
	if expectedType != "" && expectedType != mediaType {
		return Ref{}, errors.New("image media type does not match content")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return Ref{}, err
	}
	hash, size, err := l.Put(ctx, io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return Ref{}, err
	}
	if size < 1 || size > MaxImageBytes {
		return Ref{}, errors.New("image exceeds 50 MiB")
	}
	if name == "" {
		name = filepath.Base(path)
	}
	if len(name) > 1024 {
		return Ref{}, errors.New("image name exceeds 1024 bytes")
	}
	return Ref{Hash: hash, MediaType: mediaType, Bytes: size, Name: name}, nil
}
