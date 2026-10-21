package provider

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// Image is kept as bytes inside a running Worker. Callers must bound reads
// before constructing it; this layer also rejects oversized or mislabeled data.
type Image struct {
	MediaType string
	Data      []byte
}

const maxImageBytes = 1 << 20

func validateImage(image Image) error {
	if len(image.Data) == 0 || len(image.Data) > maxImageBytes {
		return errors.New("image must be 1 byte to 1 MiB")
	}
	if image.MediaType != "image/png" && image.MediaType != "image/jpeg" && image.MediaType != "image/webp" && image.MediaType != "image/gif" {
		return errors.New("unsupported image media type")
	}
	if detected := http.DetectContentType(image.Data); detected != image.MediaType {
		return errors.New("image media type does not match content")
	}
	return nil
}

func imageDataURL(image Image) (string, error) {
	if err := validateImage(image); err != nil {
		return "", err
	}
	return "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data), nil
}

func imagePayloads(message ChatMessage) ([]string, error) {
	if len(message.Images) == 0 {
		return nil, nil
	}
	if message.Role != "user" {
		return nil, errors.New("images require a user message")
	}
	result := make([]string, 0, len(message.Images))
	for _, image := range message.Images {
		encoded, err := imageDataURL(image)
		if err != nil {
			return nil, err
		}
		result = append(result, encoded)
	}
	return result, nil
}

func validateImageMessages(messages []ChatMessage) error {
	for _, message := range messages {
		if len(message.Images) == 0 {
			continue
		}
		if message.Role != "user" {
			return errors.New("images require a user message")
		}
		for _, image := range message.Images {
			if err := validateImage(image); err != nil {
				return err
			}
		}
	}
	return nil
}

func imageBase64(dataURL string) string {
	return strings.SplitN(dataURL, ",", 2)[1]
}

// The canonical history retains a tool result and its call ID. Protocols in
// this adapter set accept images as user input, so only the outgoing request
// gets an explicitly identified runtime attachment after the text tool result.
// This never creates a user message in the conversation or checkpoint.
func projectToolImages(messages []ChatMessage) []ChatMessage {
	var result []ChatMessage
	var images []Image
	var calls []string
	flush := func() {
		if len(images) != 0 {
			result = append(result, ChatMessage{Role: "user", Content: "Runtime attachments from tool calls " + strings.Join(calls, ", ") + ". These are the tools' image results, not a new user request.", Images: images})
			images, calls = nil, nil
		}
	}
	for _, message := range messages {
		if message.Role != "tool" {
			flush()
		} else if len(message.Images) != 0 {
			images = append(images, message.Images...)
			calls = append(calls, message.ToolCallID)
			message.Images = nil
		}
		result = append(result, message)
	}
	flush()
	return result
}
