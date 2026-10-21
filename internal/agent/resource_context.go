package agent

import (
	"context"
	"fmt"
	"strings"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

// Only this per-request copy contains image bytes. Canonical context and its
// checkpoints retain hashes even when the selected model cannot accept images.
func materializeResources(ctx context.Context, command workerproto.Command, messages []provider.ChatMessage) ([]provider.ChatMessage, error) {
	result := append([]provider.ChatMessage(nil), messages...)
	var library resources.Library
	var unlock func()
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	accepts := provider.SupportsImageInput(command.Model.Provider) && (command.Model.ImageInput == nil || *command.Model.ImageInput)
	for i, m := range result {
		if len(m.Resources) == 0 {
			continue
		}
		m.Images = nil
		m.Content += m.ResourceText()
		for _, ref := range m.Resources {
			if !m.VisualInput() || !accepts {
				continue
			}
			if !strings.HasPrefix(ref.MediaType, "image/") {
				continue
			}
			if ref.Bytes > 1<<20 {
				return nil, fmt.Errorf("resource %s exceeds the supported image input size", ref.Hash)
			}
			if unlock == nil {
				dir, err := instance.Dir()
				if err != nil {
					return nil, err
				}
				library = resources.Library{StateDir: dir}
				unlock, err = library.Lock(ctx)
				if err != nil {
					return nil, err
				}
			}
			data, err := library.Read(ref)
			if err != nil {
				return nil, fmt.Errorf("read image resource %s: %w", ref.Hash, err)
			}
			m.Images = append(m.Images, provider.Image{MediaType: ref.MediaType, Data: data})
		}
		if !accepts {
			m.Content += "\n[Visual content is retained but omitted from this request because the current model does not support image input.]"
		}
		result[i] = m
	}
	return result, nil
}
