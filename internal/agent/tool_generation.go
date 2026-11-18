package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"vista/internal/generation"
	"vista/internal/instance"
	"vista/internal/model"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

var generateImage = generationTool("generate_image", "Generate a still image from text. Send only material needed for this request. The finished original is stored in this Session; do not rerun a pending request.", map[string]any{"prompt": map[string]any{"type": "string"}}, []string{"prompt"})
var generateVideo = generationTool("generate_video", "Generate a short video from text or optionally one owned image as its first frame. Use a Session resource hash, not a path or URL. Only send a reference the user has authorized for the configured video connection. Do not resubmit while pending or acceptance is unknown.", map[string]any{"prompt": map[string]any{"type": "string"}, "firstFrameHash": map[string]any{"type": "string"}}, []string{"prompt"})
var cancelGeneration = generationTool("cancel_generation", "Stop one of your pending image/video tasks when the user explicitly asks. Use the exact executionId from its pending tool result. Do not replace a stopped job unless asked.", map[string]any{"executionId": map[string]any{"type": "string"}}, []string{"executionId"})
var evaluateDecision = generationTool("decision_evaluate", "Ask the configured decision model for bounded judgments on supplied text. This provides evidence, not authority to act. For choice use named alternatives; for score use ordered levels from lowest to highest and read scores as zero-based. A failure is not a negative answer.", map[string]any{
	"evidence": map[string]any{"type": "string"}, "questions": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"boolean", "choice", "score"}}, "instructions": map[string]any{"type": "string"}, "choices": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "required": []string{"id", "type", "instructions"}}},
}, []string{"evidence", "questions"})

func generationTool(name, description string, props map[string]any, required []string) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": map[string]any{"type": "object", "properties": props, "required": required}}}
}

func generationTools(command workerproto.Command, depth int) toolSet {
	if depth != 0 {
		return nil
	}
	var set toolSet
	cancel := false
	for _, t := range command.Generation {
		switch t.Kind {
		case model.ImageOutput:
			set = append(set, toolDefinition{generateImage, (*toolRuntime).generateImage, toolMain})
			cancel = true
		case model.VideoOutput:
			set = append(set, toolDefinition{generateVideo, (*toolRuntime).generateVideo, toolMain})
			cancel = true
		case model.DecisionOutput:
			set = append(set, toolDefinition{evaluateDecision, (*toolRuntime).decide, toolMain})
		}
	}
	if cancel {
		set = append(set, toolDefinition{cancelGeneration, (*toolRuntime).cancelGeneration, toolMain})
	}
	return set
}

func (r *toolRuntime) generationTarget(ctx context.Context, kind string) (model.GenerationTarget, error) {
	for _, t := range r.parent.Generation {
		if t.Kind != kind {
			continue
		}
		if r.depth != 0 {
			return t, errors.New("only the main agent can use this generation binding")
		}
		if t.Error != "" {
			return t, errors.New(t.Error)
		}
		if t.Connection != nil && t.Connection.CredentialRef != "" {
			source, err := provider.ResolveGenerationCredential(ctx, *t.Connection)
			if err != nil {
				return t, err
			}
			t.Connection = &source
		}
		return t, nil
	}
	return model.GenerationTarget{}, errors.New("this operation has no configured model")
}

func (r *toolRuntime) generateImage(ctx context.Context, call provider.ToolCall) (string, error) {
	return r.generateMedia(ctx, call, model.ImageOutput)
}
func (r *toolRuntime) generateVideo(ctx context.Context, call provider.ToolCall) (string, error) {
	return r.generateMedia(ctx, call, model.VideoOutput)
}

func (r *toolRuntime) generateMedia(ctx context.Context, call provider.ToolCall, kind string) (string, error) {
	var args struct {
		Prompt         string `json:"prompt"`
		FirstFrameHash string `json:"firstFrameHash,omitempty"`
	}
	if err := decodeArguments(call.Function.Arguments, &args); err != nil {
		return "", err
	}
	if r.parent.SessionID == "" || r.emit == nil || r.scope == nil {
		return "", errors.New("generation requires an active Session")
	}
	target, err := r.generationTarget(ctx, kind)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	id := r.parent.RunID + ":tool:" + r.parent.TaskID + "/" + call.ID
	r.scope.mediaMu.Lock()
	if r.scope.mediaStops == nil {
		r.scope.mediaStops = map[string]context.CancelFunc{}
	}
	if _, exists := r.scope.mediaStops[id]; exists {
		r.scope.mediaMu.Unlock()
		return "", errors.New("generation task is already running")
	}
	r.scope.mediaStops[id] = cancel
	r.scope.mediaMu.Unlock()
	defer func() { r.scope.mediaMu.Lock(); delete(r.scope.mediaStops, id); r.scope.mediaMu.Unlock() }()
	dir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	library := resources.Library{StateDir: dir}
	input := generation.Input{Prompt: args.Prompt}
	if args.FirstFrameHash != "" {
		if kind != model.VideoOutput || !resources.ValidHash(args.FirstFrameHash) {
			return "", errors.New("provide a valid first-frame resource hash")
		}
		db, err := r.resourceStore()
		if err != nil {
			return "", err
		}
		ref, err := db.SessionResource(ctx, r.parent.SessionID, args.FirstFrameHash)
		db.Close()
		if err != nil || !resources.ValidImageType(ref.MediaType) {
			return "", errors.New("first frame is not an image owned by this Session")
		}
		if ref.Bytes > 12<<20 {
			return "", errors.New("first-frame image exceeds the generation input limit")
		}
		// Apply the normal heavy-media admission and verify the original under the
		// same library lock used by uploads, migration and clearing.
		if err = r.emit(workerproto.Result{RunID: r.parent.RunID, Kind: "media_admission"}); err != nil {
			return "", err
		}
		unlock, err := library.Lock(ctx)
		if err != nil {
			return "", err
		}
		file, err := library.Open(ctx, ref)
		if err == nil {
			input.Frame, err = io.ReadAll(io.LimitReader(file, (12<<20)+1))
			file.Close()
		}
		unlock()
		if err != nil {
			return "", err
		}
		if int64(len(input.Frame)) != ref.Bytes {
			return "", errors.New("incomplete first-frame image")
		}
		input.FrameType = ref.MediaType
	}
	asset, err := generation.Generate(ctx, target, input)
	if err != nil {
		if errors.Is(err, generation.ErrUpstreamCanceled) {
			r.mediaStopped = "canceled"
			return "", err
		}
		if ctx.Err() != nil {
			r.mediaStopped = "stopped"
		}
		return "", err
	}
	defer asset.Body.Close()
	file, ref, err := library.StageGenerated(ctx, asset.Body, kind == model.VideoOutput)
	if err != nil {
		if ctx.Err() != nil {
			r.mediaStopped = "stopped"
		}
		return "", err
	}
	defer func() { file.Close(); _ = os.Remove(file.Name()) }()
	unlock, err := library.Lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	stamp, err := library.Stamp(ctx)
	if err != nil {
		return "", err
	}
	hash, n, err := library.Put(ctx, file)
	if err != nil {
		return "", err
	}
	if n != ref.Bytes {
		return "", errors.New("generated original size changed during import")
	}
	ref.Hash = hash
	if err = ctx.Err(); err != nil {
		r.mediaStopped = "stopped"
		return "", err
	}
	if err = r.emit(workerproto.Result{RunID: r.parent.RunID, Kind: "resource_import", Resource: &ref, ResourceStorage: &stamp}); err != nil {
		return "", err
	}
	r.generated = &ref
	return marshalResult(map[string]any{"resource": r.resourceLink(ref), "model": target.Model.ID, "provider": target.Model.ProviderID})
}

func (r *toolRuntime) cancelGeneration(ctx context.Context, call provider.ToolCall) (string, error) {
	var args struct {
		ExecutionID string `json:"executionId"`
	}
	if err := decodeArguments(call.Function.Arguments, &args); err != nil {
		return "", err
	}
	if r.scope == nil || r.depth != 0 {
		return "", errors.New("generation task is not owned by this agent")
	}
	r.scope.mediaMu.Lock()
	stop := r.scope.mediaStops[args.ExecutionID]
	r.scope.mediaMu.Unlock()
	if stop == nil {
		return "", errors.New("no pending generation for that execution ID")
	}
	stop()
	return marshalResult(map[string]any{"executionId": args.ExecutionID, "status": "stop_requested", "note": "Local cancellation requested; upstream cancellation is not assumed."})
}

func (r *toolRuntime) decide(ctx context.Context, call provider.ToolCall) (string, error) {
	var args struct {
		Evidence  string                `json:"evidence"`
		Questions []generation.Question `json:"questions"`
	}
	if err := decodeArguments(call.Function.Arguments, &args); err != nil {
		return "", err
	}
	target, err := r.generationTarget(ctx, model.DecisionOutput)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := generation.Decide(ctx, target, args.Evidence, args.Questions)
	if err != nil {
		return "", err
	}
	ref, err := recordToolOutput(ctx, r.parent.RunID, call.ID, out, r.emit)
	if err != nil {
		return "", err
	}
	return marshalResult(map[string]any{"artifactId": ref.ArtifactID, "outputRef": ref, "result": json.RawMessage(out), "model": target.Model.ID, "provider": target.Model.ProviderID})
}
