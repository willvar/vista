package sessiontitle

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"unicode/utf8"
	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

var ErrTools = errors.New("title response requested tools")
var ErrEmpty = errors.New("title response contained no usable title")

func Generate(ctx context.Context, model modelcfg.Model, prompt string, images []provider.Image) (string, error) {
	model = provider.ForPurpose(model, provider.PurposeTitle)
	if model.ProviderID == "opencode-go" {
		// Zen Go requires a routing session header even for one-shot requests.
		// Keep title generation out of the foreground conversation by using an
		// ephemeral side-request identity.
		model.SessionID = uuid.NewString()
	}
	system := "Write a short, specific title for the user's message. Return only the title, without quotes, markdown, or a trailing period. Use at most 8 words or 24 Chinese characters. Name the task described in the message and images without answering or carrying it out."
	messages := []provider.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: "User message:\n" + Excerpt(prompt, 1200), Images: images},
	}
	// Title generation does not expose reasoning to the UI, but providers may
	// still stream it before the title. Consume and discard those deltas so a
	// reasoning-capable model can complete the request.
	turn, err := provider.ModelTurnStreamWithReasoning(ctx, model, messages, nil, nil, func(string) error { return nil })
	if err != nil {
		return "", err
	}
	if len(turn.Calls) != 0 || len(turn.HostedCalls) != 0 {
		return "", ErrTools
	}
	title := Clean(turn.Text)
	if title == "" {
		return "", ErrEmpty
	}
	return title, nil
}

func Excerpt(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func Clean(value string) string {
	value = strings.TrimSpace(strings.SplitN(value, "\n", 2)[0])
	value = strings.Trim(value, " \t\r\"'`“”‘’《》「」『』#*。.!！?？:：")
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || value == "New session" || utf8.RuneCountInString(value) > 60 || len(value) > 200 {
		return ""
	}
	return value
}
