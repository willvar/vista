package resources

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const MaxVideoBytes = 128 << 20

func ValidResourceSize(mediaType string, size int64) bool {
	if size < 1 {
		return false
	}
	if ValidImageType(mediaType) {
		return size <= MaxImageBytes
	}
	return (mediaType == "video/mp4" || mediaType == "video/webm") && size <= MaxVideoBytes
}

// ImportGenerated reuses the original-byte resource library. Stage the transfer
// before taking the library lock; the caller keeps that lock through ownership
// commit. A failed transfer never exposes a partial result in the Session.
func (l Library) StageGenerated(ctx context.Context, input io.Reader, video bool) (*os.File, Ref, error) {
	r := bufio.NewReaderSize(input, 512)
	prefix, _ := r.Peek(512)
	media := http.DetectContentType(prefix)
	limit := int64(MaxImageBytes)
	if video {
		limit = MaxVideoBytes
		if media != "video/mp4" && media != "video/webm" {
			return nil, Ref{}, errors.New("generation did not return supported video bytes")
		}
	} else if !ValidImageType(media) {
		return nil, Ref{}, errors.New("generation did not return supported image bytes")
	}
	dir := filepath.Join(l.StateDir, "resource-staging")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, Ref{}, err
	}
	file, err := os.CreateTemp(dir, "generation-*")
	if err != nil {
		return nil, Ref{}, err
	}
	ready := false
	defer func() {
		if !ready {
			file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	n, err := io.Copy(file, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, Ref{}, err
	}
	if err = ctx.Err(); err != nil {
		return nil, Ref{}, err
	}
	if !ValidResourceSize(media, n) {
		return nil, Ref{}, errors.New("generated media is empty or exceeds the resource size limit")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, Ref{}, err
	}
	name := map[string]string{"image/png": "generated.png", "image/jpeg": "generated.jpg", "image/webp": "generated.webp", "image/gif": "generated.gif", "video/mp4": "generated.mp4", "video/webm": "generated.webm"}[media]
	ready = true
	return file, Ref{MediaType: media, Name: name, Bytes: n}, nil
}
