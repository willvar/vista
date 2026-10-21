package workerproto

import (
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

// ReplyLanguageName accepts only UI locale identifiers, never prompt text.
func ReplyLanguageName(code string) string {
	return replyLanguageNames[code]
}
