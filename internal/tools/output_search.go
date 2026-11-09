package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type OutputSearchMatch struct {
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"nextOffset"`
	Text          string `json:"text"`
	Context       bool   `json:"context,omitempty"`
	TextTruncated bool   `json:"textTruncated,omitempty"`
}

type OutputSearchPage struct {
	Offset         int64               `json:"offset"`
	NextOffset     int64               `json:"nextOffset"`
	AvailableBytes int64               `json:"availableBytes"`
	Matches        []OutputSearchMatch `json:"matches"`
	Count          int                 `json:"count"`
	Truncated      bool                `json:"truncated"`
}

// Search existing output without rerunning its producer. Normal lines are
// matched from the read buffer; unusually long lines use a ReaderAt view so
// memory stays bounded without imposing a line-length ceiling on matching.
func SearchOutput(ctx context.Context, stateDir, path string, offset int64, limit int, pattern string, literal, ignoreCase bool, contextLines int) (OutputSearchPage, error) {
	limit, err := OutputPageLimit(limit)
	if err != nil {
		return OutputSearchPage{}, err
	}
	if contextLines < 0 || contextLines > MaxToolResultLines {
		return OutputSearchPage{}, errors.New("contextLines must be between 0 and 2000")
	}
	match, err := textMatcher(pattern, literal, ignoreCase)
	if err != nil {
		return OutputSearchPage{}, err
	}
	file, err := openOutput(stateDir, path)
	if err != nil {
		return OutputSearchPage{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return OutputSearchPage{}, err
	}
	if !info.Mode().IsRegular() || offset < 0 || offset > info.Size() {
		return OutputSearchPage{}, errors.New("invalid output range")
	}
	page := OutputSearchPage{Offset: offset, NextOffset: offset, AvailableBytes: info.Size(), Matches: []OutputSearchMatch{}}
	reader := bufio.NewReader(io.NewSectionReader(file, offset, info.Size()-offset))
	var previous []OutputSearchMatch
	after, used := 0, 0
	lastIncluded := int64(-1)
	appendMatch := func(item OutputSearchMatch) bool {
		if item.Offset <= lastIncluded {
			return true
		}
		encoded, _ := json.Marshal(item)
		if len(page.Matches) > 0 && used+len(encoded) > limit {
			return false
		}
		page.Matches = append(page.Matches, item)
		used += len(encoded)
		lastIncluded = item.Offset
		if !item.Context {
			page.Count++
		}
		return true
	}
	for page.NextOffset < info.Size() {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		start := page.NextOffset
		var text strings.Builder
		var length int64
		for {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			fragment, readErr := reader.ReadSlice('\n')
			length += int64(len(fragment))
			if text.Len() < MaxOutputPageBytes {
				text.Write(fragment[:min(len(fragment), MaxOutputPageBytes-text.Len())])
			}
			if readErr == bufio.ErrBufferFull {
				continue
			}
			if readErr != nil && readErr != io.EOF {
				return page, readErr
			}
			break
		}
		page.NextOffset = start + length
		full := text.String()
		hit := match.matches(strings.TrimSuffix(full, "\n"))
		if length > int64(len(full)) {
			hit = match.expression.MatchReader(bufio.NewReader(webContextReader{ctx, io.NewSectionReader(file, start, length)}))
		}
		item := OutputSearchMatch{Offset: start, NextOffset: page.NextOffset, Text: searchPreview(strings.ToValidUTF8(strings.TrimSuffix(full, "\n"), "�")), Context: !hit, TextTruncated: length > 512}
		if hit {
			// Reserve the hit before adding context; a full page always resumes
			// at an unreturned hit, never after silently consuming it.
			savedLen, savedUsed, savedLast, savedCount := len(page.Matches), used, lastIncluded, page.Count
			for _, before := range previous {
				if !appendMatch(before) {
					break
				}
			}
			if !appendMatch(item) {
				page.Matches = page.Matches[:savedLen]
				used = savedUsed
				lastIncluded = savedLast
				page.Count = savedCount
				if savedLen == 0 {
					appendMatch(item)
				} else {
					page.NextOffset = start
					page.Truncated = true
					return page, nil
				}
			}
			after = contextLines
		} else if after > 0 {
			if !appendMatch(item) {
				page.NextOffset = start
				page.Truncated = true
				return page, nil
			}
			after--
		}
		if contextLines > 0 {
			previous = append(previous, item)
			if len(previous) > contextLines {
				previous = previous[1:]
			}
		}
		if page.NextOffset-offset >= maxSearchBytes && page.NextOffset < info.Size() {
			page.Truncated = true
			break
		}
	}
	return page, nil
}
