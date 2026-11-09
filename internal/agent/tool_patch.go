package agent

import (
	"context"
	"encoding/json"
	"io"

	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/tools"
)

var applyPatchSchema = map[string]any{"type": "function", "function": map[string]any{
	"name":        "apply_patch",
	"description": "Apply standard unified diff (---/+++/@@ with accurate line counts) or Git text diff. directory is absolute; patch paths are relative to it (a/ and b/ prefixes accepted). expectedSha256 maps every affected relative path to its hash from read_file, or missing for new paths. Supports multiple hunks/files, creation, deletion and Git renames. All rules, hashes and hunks are checked before project writes. Files commit atomically one by one; inspect applied/unapplied after an I/O failure before retrying. Returns saved diff artifacts for the actual writes. Binary patches and nonregular files are unsupported.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"directory": map[string]any{"type": "string"}, "patch": map[string]any{"type": "string"},
		"expectedSha256": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	}, "required": []string{"directory", "patch", "expectedSha256"}},
}}

func (r *toolRuntime) applyPatch(ctx context.Context, call provider.ToolCall) (string, error) {
	var input tools.PatchInput
	if err := decodeArguments(call.Function.Arguments, &input); err != nil {
		return "", err
	}
	stateDir, err := instance.Dir()
	if err != nil {
		return "", err
	}
	result, applyErr := tools.ApplyPatch(ctx, stateDir, input)
	if len(result.Files) > 0 && r.emit != nil {
		ref, saveErr := recordToolOutputStream(ctx, r.parent.RunID, call.ID, func(w io.Writer) error { return json.NewEncoder(w).Encode(result) }, r.emit)
		if saveErr == nil {
			files := make([]map[string]any, 0, len(result.Files))
			for _, file := range result.Files {
				files = append(files, map[string]any{"path": file.Path, "oldPath": file.OldPath, "sha256": file.SHA256, "deleted": file.Deleted})
			}
			output, err := marshalResult(map[string]any{"files": files, "applied": result.Applied, "unapplied": result.Unapplied, "artifactId": ref.ArtifactID, "bytes": ref.Bytes})
			if err != nil {
				return "", err
			}
			return output, applyErr
		}
	}
	for i := range result.Files {
		result.Files[i].Diff.Unified = ""
		result.Files[i].Diff.Omitted = "unavailable"
	}
	output, err := marshalResult(result)
	if err != nil {
		return "", err
	}
	return output, applyErr
}
