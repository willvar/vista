// Package replylanguage owns the default-language policy shared by model requests.
package replylanguage

import (
	"context"
	_ "embed"
	"encoding/json"
)

// Generated from web/src/i18n/languages.ts by npm run generate:i18n.
//
//go:embed language_names.json
var languageNamesJSON []byte

var replyLanguageNames = func() map[string]string {
	var names map[string]string
	if err := json.Unmarshal(languageNamesJSON, &names); err != nil {
		panic(err)
	}
	return names
}()

// Name accepts only UI locale identifiers, never prompt text.
func Name(code string) string {
	return replyLanguageNames[code]
}

type defaultLanguageKey struct{}

// WithDefault carries request metadata across calls without changing model
// configuration or conversation history. An empty code uses the message language.
func WithDefault(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, defaultLanguageKey{}, code)
}

// Instruction is applied by the shared request preparation path, never by task
// prompts. Bare protocol callers without Vista request metadata are unchanged.
func Instruction(ctx context.Context) string {
	code, configured := ctx.Value(defaultLanguageKey{}).(string)
	if !configured {
		return ""
	}
	if language := Name(code); language != "" {
		return "Default to replies in " + language + "; follow the user's explicit language requests."
	}
	return "Use the same language as the user's message; follow the user's explicit language requests."
}
