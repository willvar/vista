package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/store"
)

var listResourcesSchema = map[string]any{"type": "function", "function": map[string]any{"name": "list_resources", "description": "List retained image resources in this Session, including attachments omitted by context compaction. Returns stable hashes, names, media types and sizes. Use read_resource to see an image, and nextAfter to continue listing.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"after": map[string]any{"type": "string"}}}}}
var readResourceSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_resource", "description": "Read an original image retained by this Session by its exact hash. The image is supplied only to the next model request as visual input; call again to inspect it in a later request. Reading metadata alone does not inspect it. Requires an image-capable model.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"hash": map[string]any{"type": "string"}}, "required": []string{"hash"}}}}

func (r *toolRuntime) resourceStore() (*store.Store, error) {
	if r.parent.SessionID == "" {
		return nil, errors.New("Session resources are unavailable")
	}
	dir, err := instance.Dir()
	if err != nil {
		return nil, err
	}
	return store.OpenReadOnly(filepath.Join(dir, "vista.db"))
}

func (r *toolRuntime) listResources(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		After string `json:"after"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if input.After != "" && !resources.ValidHash(input.After) {
		return "", errors.New("invalid resource cursor")
	}
	db, err := r.resourceStore()
	if err != nil {
		return "", err
	}
	defer db.Close()
	refs, err := db.SessionResources(ctx, r.parent.SessionID, input.After, 17)
	if err != nil {
		return "", err
	}
	more := len(refs) > 16
	if more {
		refs = refs[:16]
	}
	next := ""
	if more {
		next = refs[len(refs)-1].Hash
	}
	return marshalResult(map[string]any{"resources": refs, "nextAfter": next, "hasMore": more})
}

func (r *toolRuntime) readResource(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Hash string `json:"hash"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if !resources.ValidHash(input.Hash) {
		return "", errors.New("invalid resource hash")
	}
	if !provider.SupportsImageInput(r.parent.Model.Provider) || r.parent.Model.ImageInput != nil && !*r.parent.Model.ImageInput {
		return "", errors.New("current model does not support image input; select an image-capable model to view this resource")
	}
	db, err := r.resourceStore()
	if err != nil {
		return "", err
	}
	defer db.Close()
	ref, err := db.SessionResource(ctx, r.parent.SessionID, input.Hash)
	if err != nil {
		return "", fmt.Errorf("resource is not available in this Session: %w", err)
	}
	if !strings.HasPrefix(ref.MediaType, "image/") {
		return "", errors.New("this resource type cannot be supplied to the current model")
	}
	if _, err := db.ResolveImages(ctx, []resources.Ref{ref}); err != nil {
		return "", fmt.Errorf("read resource: %w", err)
	}
	r.resourceResults = []resources.Ref{ref}
	return marshalResult(map[string]any{"resource": ref, "visualInput": true})
}
