package workerproto

// Results use bounded frames; image-bearing commands are streamed without an
// aggregate size cap and validate each image and the prompt independently.
const (
	MaxResultFrame = 64 << 10
	MaxPromptBytes = 8 << 10
)
