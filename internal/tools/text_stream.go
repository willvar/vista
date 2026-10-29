package tools

import (
	"bufio"
	"context"
	"errors"
	"io"
	"unicode/utf8"
)

// scanText supplies bounded fragments, with end=true exactly once per source
// line. A trailing newline introduces a final blank line, like strings.Split.
// Validation also covers unselected portions of the file.
func scanText(ctx context.Context, input io.Reader, visit func([]byte, bool) error) error {
	reader := bufio.NewReaderSize(input, 32<<10)
	var tail []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fragment, err := reader.ReadSlice('\n')
		final := err == io.EOF
		joined := fragment
		if len(tail) != 0 {
			joined = append(tail, fragment...)
		}
		cut := len(joined)
		if !final {
			for cut > 0 && cut > len(joined)-utf8.UTFMax && !utf8.RuneStart(joined[cut-1]) {
				cut--
			}
			if cut > 0 && !utf8.FullRune(joined[cut-1:]) {
				cut--
			} else {
				cut = len(joined)
			}
		}
		if !utf8.Valid(joined[:cut]) {
			return errors.New("file is not UTF-8 text")
		}
		tail = append(tail[:0], joined[cut:]...)
		end := final || err == nil
		if err == nil {
			fragment = fragment[:len(fragment)-1]
		}
		if len(fragment) != 0 || end {
			if visitErr := visit(fragment, end); visitErr != nil {
				return visitErr
			}
		}
		if final {
			return nil
		}
		if err != nil && err != bufio.ErrBufferFull {
			return err
		}
	}
}
