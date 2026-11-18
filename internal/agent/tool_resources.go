package agent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"vista/internal/workerproto"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/store"
)

var listResourcesSchema = map[string]any{"type": "function", "function": map[string]any{"name": "list_resources", "description": "List retained image and video resources in this Session, including attachments omitted by context compaction. Returns stable hashes, names, media types, sizes and browser URLs. To display an image in a reply, use ![description](url) with the returned url, never a filesystem path or file: URL. Use read_resource to see an image, and nextAfter to continue listing.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"after": map[string]any{"type": "string"}}}}}
var readResourceSchema = map[string]any{"type": "function", "function": map[string]any{"name": "read_resource", "description": "Inspect an original image by exactly one of: an absolute local path, or an exact resource hash already owned by this Session. Local images (including downloaded images and previously extracted video frames) are copied unchanged into the shared resource library, up to 50 MiB each. To display the image in a reply, use ![description](url) with the returned resource.url, never a filesystem path or file: URL. Visual input is supplied only to the next image-capable model request; call again for later inspection. Does not extract video frames.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "hash": map[string]any{"type": "string"}}}}}

// URLs are tool presentation data; canonical references retain only identity
// and ownership metadata and remain independent of the HTTP route.
type resourceLink struct {
	resources.Ref
	URL string `json:"url"`
}

func (r *toolRuntime) resourceLink(ref resources.Ref) resourceLink {
	return resourceLink{Ref: ref, URL: "/api/sessions/" + url.PathEscape(r.parent.SessionID) + "/resources/" + ref.Hash}
}

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
	links := make([]resourceLink, len(refs))
	for i, ref := range refs {
		links[i] = r.resourceLink(ref)
	}
	return marshalResult(map[string]any{"resources": links, "nextAfter": next, "hasMore": more})
}

func (r *toolRuntime) readResource(ctx context.Context, call provider.ToolCall) (string, error) {
	var input struct {
		Hash string `json:"hash"`
		Path string `json:"path"`
	}
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	if (input.Path == "") == (input.Hash == "") || input.Hash != "" && !resources.ValidHash(input.Hash) || input.Path != "" && !filepath.IsAbs(input.Path) {
		return "", errors.New("provide exactly one absolute path or exact resource hash")
	}
	if !provider.SupportsImageInput(r.parent.Model.Provider) || r.parent.Model.ImageInput != nil && !*r.parent.Model.ImageInput {
		return "", errors.New("current model does not support image input; select an image-capable model to view this resource")
	}
	dir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	if r.emit != nil {
		if err = r.emit(workerproto.Result{RunID: r.parent.RunID, Kind: "media_admission"}); err != nil {
			return "", err
		}
	}
	library := resources.Library{StateDir: dir}
	unlock, err := library.Lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	stamp, err := library.Stamp(ctx)
	if err != nil {
		return "", err
	}
	var ref resources.Ref
	if input.Path != "" {
		if r.parent.SessionID == "" {
			return "", errors.New("Session resources are unavailable")
		}
		ref, err = library.ImportImage(ctx, input.Path, "", "")
		if err != nil {
			return "", err
		}
	} else {
		db, openErr := r.resourceStore()
		if openErr != nil {
			return "", openErr
		}
		defer db.Close()
		ref, err = db.SessionResource(ctx, r.parent.SessionID, input.Hash)
		if err != nil {
			return "", fmt.Errorf("resource is not available in this Session: %w", err)
		}
		if !strings.HasPrefix(ref.MediaType, "image/") {
			return "", errors.New("resource is not an image")
		}
		file, openErr := library.Open(ctx, ref)
		if openErr != nil {
			return "", openErr
		}
		file.Close()
	}
	if r.emit != nil {
		if err = r.emit(workerproto.Result{RunID: r.parent.RunID, Kind: "resource_import", Resource: &ref, ResourceStorage: &stamp}); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.resourceResults = []resources.Ref{ref}
	return marshalResult(map[string]any{"resource": r.resourceLink(ref), "visualInput": true})
}
