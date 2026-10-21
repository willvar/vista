package provider

import (
	"bufio"
	"errors"
	"strings"
)

// SSE permits one optional space after the data field's colon. Keep the
// payload otherwise unchanged so each protocol can enforce its own bounds.
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
func nextSSEData(scanner *bufio.Scanner, limit int) (string, bool, error) {
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
		separator := 0
		if sawData {
			separator = 1
		}
		if data.Len()+separator+len(fragment) > limit {
			return "", false, errors.New("SSE event exceeds limit")
		}
		if separator != 0 {
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
