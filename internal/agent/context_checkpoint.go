package agent

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// Gob preserves resource references, signed thinking and provider-owned replay fields whose
// JSON tags intentionally exclude them from ordinary chat API serialization.
// Checkpoints are immutable files; the acknowledged context_notice commits the
// pointer. A failed/canceled compaction therefore cannot replace usable context.
type contextCheckpoint struct {
	Model    string
	Messages []provider.ChatMessage
}

func saveContextCheckpoint(ctx context.Context, command workerproto.Command, messages []provider.ChatMessage) (*workerproto.ContextCheckpoint, error) {
	path, err := saveContextFile(command, messages[1:], "context-*.gob")
	if err != nil {
		return nil, err
	}
	return &workerproto.ContextCheckpoint{Path: path, ThroughSequence: command.MessageSequence, InputTokens: provider.EstimateRequestTokens(ctx, messages, nil)}, nil
}

func saveContextFile(command workerproto.Command, messages []provider.ChatMessage, pattern string) (string, error) {
	for _, m := range messages {
		if len(m.Images) > 0 {
			return "", errors.New("model history requires resource references, not image bytes")
		}
	}
	dir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "outputs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	err = gob.NewEncoder(f).Encode(contextCheckpoint{Model: compactionModelKey(command.Model), Messages: messages})
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func loadContextFile(root *os.Root, path string, command workerproto.Command) ([]provider.ChatMessage, error) {
	rel, err := filepath.Rel(root.Name(), path)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("load model history: %w", err)
	}
	defer file.Close()
	var saved contextCheckpoint
	if err := gob.NewDecoder(file).Decode(&saved); err != nil {
		return nil, fmt.Errorf("decode model history: %w", err)
	}
	if saved.Model != compactionModelKey(command.Model) {
		for i, m := range saved.Messages {
			// Provider-owned signatures belong to their original endpoint/model.
			for j := range m.ToolCalls {
				m.ToolCalls[j].ThoughtSignature = ""
				m.ToolCalls[j].GeminiID = ""
			}
			saved.Messages[i] = provider.ChatMessage{Role: m.Role, Content: m.Content, Resources: m.Resources, ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls, RuntimeData: m.RuntimeData}
		}
	}
	return saved.Messages, nil
}

func loadConversationContext(ctx context.Context, command workerproto.Command, depth int) ([]provider.ChatMessage, error) {
	var messages []provider.ChatMessage
	if depth != 0 || command.SessionID == "" || command.MessageSequence == 0 {
		for _, m := range command.History {
			messages = append(messages, provider.ChatMessage{Role: m.Role, Content: m.Content, Resources: m.Images})
		}
		return messages, nil
	}
	dir, err := instance.Dir()
	if err != nil {
		return nil, err
	}
	db, err := store.OpenReadOnly(filepath.Join(dir, "vista.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	checkpoint, err := db.ContextCheckpoint(ctx, command.SessionID)
	if err != nil {
		return nil, err
	}
	var root *os.Root
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	load := func(path string) ([]provider.ChatMessage, error) {
		if root == nil {
			var err error
			root, err = os.OpenRoot(filepath.Join(dir, "outputs"))
			if err != nil {
				return nil, err
			}
		}
		return loadContextFile(root, path, command)
	}
	if checkpoint != nil {
		if checkpoint.ThroughSequence > command.MessageSequence {
			return nil, errors.New("conversation context is newer than the requested input")
		}
		messages, err = load(checkpoint.Path)
		if err != nil {
			return nil, err
		}
	}
	before := command.MessageSequence
	if command.Action == "compact" {
		// Interrupted runs may have retained tools after the last formal input.
		before = 0
	}
	entries, err := db.ContextEntries(ctx, command.SessionID, checkpoint, before)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.History != nil {
			retained, err := load(entry.History.History.Path)
			if err != nil {
				return nil, err
			}
			messages = append(messages, retained...)
			continue
		}
		m := entry.Message
		if m.OutputBytes > len(m.Content) {
			full, err := db.ReadFullModelOutput(ctx, command.SessionID, m.RunID, m.OutputTurn)
			if err != nil {
				return nil, err
			}
			m.Content = full.Content
		}
		images, err := db.MessageResources(ctx, command.SessionID, m.ID)
		if err != nil {
			return nil, err
		}
		message := provider.ChatMessage{Role: m.Role, Content: m.Content, Resources: images}
		if m.QuestionCallID != "" {
			message.Role, message.ToolCallID = "tool", m.QuestionCallID
		}
		messages = append(messages, message)
	}
	return completeInterruptedToolHistory(messages, command.QuestionCallID), ctx.Err()
}
