package provider

import (
	"bufio"
	"io"
	"strings"
)

// Upstream model events can contain complete answers and opaque reasoning
// snapshots. Read lines without Scanner's token ceiling; response allocations
// belong to the conversation Worker and its memory budget, not an SSE byte cap.
type sseLines struct {
	reader *bufio.Reader
	line   string
	err    error
}

func newSSELines(reader io.Reader) *sseLines {
	return &sseLines{reader: bufio.NewReader(reader)}
}

func (lines *sseLines) Scan() bool {
	if lines.err != nil {
		return false
	}
	lines.line, lines.err = lines.reader.ReadString('\n')
	if lines.line == "" {
		return false
	}
	lines.line = strings.TrimSuffix(strings.TrimSuffix(lines.line, "\n"), "\r")
	return true
}

func (lines *sseLines) Text() string { return lines.line }

func (lines *sseLines) Err() error {
	if lines.err == io.EOF {
		return nil
	}
	return lines.err
}

// SSE permits one optional space after the data field's colon. Keep the
// payload otherwise unchanged for protocol validation.
func sseDataLine(line string) (string, bool) {
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	data = strings.TrimPrefix(data, " ")
	return data, true
}

// Keepalives are complete SSE payloads, not individual data fields. Filtering
// a field before joining multiline events can turn invalid JSON into valid JSON.
func sseKeepalive(data string) bool {
	return data == "null" || data == ": keepalive"
}

// nextSSEData assembles one event from one or more data fields. It is used by
// protocols that consume complete SSE events rather than individual deltas.
func nextSSEData(scanner *sseLines) (string, bool, error) {
	var data strings.Builder
	sawData := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if sawData {
				if data.Len() > 0 && !sseKeepalive(data.String()) {
					return data.String(), true, nil
				}
				data.Reset()
				sawData = false
			}
			continue
		}
		fragment, ok := sseDataLine(line)
		if !ok {
			continue
		}
		if sawData {
			data.WriteByte('\n')
		}
		data.WriteString(fragment)
		sawData = true
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	if sawData && data.Len() > 0 && !sseKeepalive(data.String()) {
		return data.String(), true, nil
	}
	return "", false, nil
}
