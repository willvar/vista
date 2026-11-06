// Package toolinput defines the shared byte budget and recoverable input errors
// used by model adapters and native tools.
package toolinput

import (
	"fmt"
	"strings"
)

// MaxArgumentsBytes includes the complete JSON object, not just one text field.
const MaxArgumentsBytes = 256 << 10

type Error struct {
	Field       string
	ActualBytes int
	MaxBytes    int
	Hint        string
}

func (e *Error) Error() string {
	return fmt.Sprintf("invalid %s: received %d UTF-8 bytes; expected nonempty input within %d bytes. No operation was executed. %s", e.Field, e.ActualBytes, e.MaxBytes, e.Hint)
}

func CheckText(field, value string, maximum int, hint string) error {
	if strings.TrimSpace(value) == "" || len(value) > maximum {
		return &Error{Field: field, ActualBytes: len(value), MaxBytes: maximum, Hint: hint}
	}
	return nil
}
