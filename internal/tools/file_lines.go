package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// LinePage holds full-file identity and the complete selected line range.
// Selected numbered text is spooled to disk, so even a huge single line uses
// bounded memory. The caller owns and removes the temporary file.
type LinePage struct {
	Oversized                      bool
	Path                           string
	SHA256                         string
	StartLine, EndLine, TotalLines int
	Bytes                          int64
}

func ReadLines(ctx context.Context, path string, start, count, pageBytes int, output *os.File) (LinePage, error) {
	path, err := absolute(path)
	if err != nil {
		return LinePage{}, err
	}
	if start < 1 || count < 1 || pageBytes < 1 {
		return LinePage{}, errors.New("invalid line range")
	}
	f, err := os.Open(path)
	if err != nil {
		return LinePage{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return LinePage{}, err
	}
	if !info.Mode().IsRegular() {
		return LinePage{}, errors.New("file must be regular")
	}
	page := LinePage{Path: path, StartLine: start}
	hash := sha256.New()
	line, selected, lineStart, written := 1, 0, int64(0), int64(0)
	accepting := true
	lineOpen := false
	escapedBytes := 0
	err = scanText(ctx, io.TeeReader(f, hash), func(fragment []byte, end bool) error {
		page.Bytes += int64(len(fragment))
		if line >= start && selected < count && accepting {
			if !lineOpen {
				lineStart = written
				escapedBytes += len(fmt.Sprint(line)) + 2
				if selected > 0 {
					escapedBytes += 2
				}
				if selected > 0 {
					n, e := output.WriteString("\n")
					written += int64(n)
					if e != nil {
						return e
					}
				}
				n, e := fmt.Fprintf(output, "%d: ", line)
				written += int64(n)
				if e != nil {
					return e
				}
				lineOpen = true
			}
			n, e := output.Write(fragment)
			written += int64(n)
			if e != nil {
				return e
			}
			escaped, _ := json.Marshal(string(fragment))
			escapedBytes += len(escaped) - 2
			if escapedBytes > pageBytes && selected > 0 {
				if e := output.Truncate(lineStart); e != nil {
					return e
				}
				if _, e := output.Seek(lineStart, io.SeekStart); e != nil {
					return e
				}
				written, accepting = lineStart, false
			}
			if end && accepting {
				selected++
				if escapedBytes > pageBytes {
					page.Oversized = true
				}
				page.EndLine = line
				if escapedBytes >= pageBytes {
					accepting = false
				}
			}
		}
		if end {
			line++
			lineOpen = false
		}
		return nil
	})
	if err != nil {
		return LinePage{}, err
	}
	// The tee includes delimiters even though visit receives content fragments.
	page.Bytes, err = f.Seek(0, io.SeekCurrent)
	if err != nil {
		return LinePage{}, err
	}
	page.TotalLines = line - 1
	page.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if start > page.TotalLines {
		return LinePage{}, fmt.Errorf("startLine %d is beyond the end of the file (%d lines)", start, page.TotalLines)
	}
	return page, nil
}
