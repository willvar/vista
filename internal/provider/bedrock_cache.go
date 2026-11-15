package provider

// Claude support/TTL exceptions follow OpenCode's bedrock-cache.ts at
// 6dd40633a15ec82c5fa2f2c4ea17e22c2ba67084; see licenses/opencode/LICENSE.

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	modelcfg "vista/internal/model"
)

var bedrockCache5m = []string{
	"anthropic.claude-3-5-sonnet-20241022-v2:0",
	"anthropic.claude-3-5-haiku-20241022-v1:0",
	"anthropic.claude-3-7-sonnet-20250219-v1:0",
	"anthropic.claude-sonnet-4-20250514-v1:0",
	"anthropic.claude-opus-4-20250514-v1:0",
	"anthropic.claude-opus-4-1-20250805-v1:0",
}

func bedrockShortCache(id string) bool {
	for _, legacy := range bedrockCache5m {
		if strings.Contains(id, legacy) {
			return true
		}
	}
	return false
}

func supportedBedrockCache(id string) bool {
	if bedrockShortCache(id) {
		return true
	}
	if !strings.Contains(id, "anthropic.claude-") {
		return false
	}
	for _, legacy := range []string{"anthropic.claude-instant", "anthropic.claude-v1", "anthropic.claude-v2", "anthropic.claude-3-"} {
		if strings.Contains(id, legacy) {
			return false
		}
	}
	return true
}

func applyBedrockCache(model modelcfg.Model, input *bedrockruntime.ConverseStreamInput) error {
	if err := ValidatePromptCaching(model); err != nil {
		return err
	}
	if promptCacheCapabilities(model).format != "bedrock" {
		return nil
	}
	point := types.CachePointBlock{Type: types.CachePointTypeDefault}
	// Older Claude releases (and Nova explicit overrides) reject even a 5m ttl
	// field. Omission uses their native five-minute default.
	if model.PromptCaching != nil && model.PromptCaching.TTL == "1h" && !bedrockShortCache(model.ID) && !strings.Contains(model.ID, "amazon.nova") {
		point.Ttl = types.CacheTTL("1h")
	}
	remaining := 4
	for _, block := range input.System {
		if _, ok := block.(*types.SystemContentBlockMemberCachePoint); ok {
			remaining--
		}
	}
	if input.ToolConfig != nil {
		for _, tool := range input.ToolConfig.Tools {
			if _, ok := tool.(*types.ToolMemberCachePoint); ok {
				remaining--
			}
		}
	}
	for _, msg := range input.Messages {
		for _, block := range msg.Content {
			if _, ok := block.(*types.ContentBlockMemberCachePoint); ok {
				remaining--
			}
		}
	}
	if remaining < 0 {
		return errCacheBreakpoints
	}
	if input.ToolConfig != nil && len(input.ToolConfig.Tools) > 0 && !strings.Contains(model.ID, "amazon.nova") && remaining > 0 {
		if _, exists := input.ToolConfig.Tools[len(input.ToolConfig.Tools)-1].(*types.ToolMemberCachePoint); !exists {
			input.ToolConfig.Tools = append(input.ToolConfig.Tools, &types.ToolMemberCachePoint{Value: point})
			remaining--
		}
	}
	system := make([]types.SystemContentBlock, 0, len(input.System)+2)
	for i, block := range input.System {
		system = append(system, block)
		_, text := block.(*types.SystemContentBlockMemberText)
		if text && (i == 0 || i == len(input.System)-1) && remaining > 0 {
			if i+1 < len(input.System) {
				if _, exists := input.System[i+1].(*types.SystemContentBlockMemberCachePoint); exists {
					continue
				}
			}
			system = append(system, &types.SystemContentBlockMemberCachePoint{Value: point})
			remaining--
		}
	}
	input.System = system
	for i := len(input.Messages) - 1; i >= 0 && remaining > 0; i-- {
		blocks := input.Messages[i].Content
		target := -1
		for j, block := range blocks {
			if _, marked := block.(*types.ContentBlockMemberCachePoint); marked {
				target = -1
				break
			}
			if _, text := block.(*types.ContentBlockMemberText); text {
				target = j
			}
		}
		if target < 0 {
			for j := len(blocks) - 1; j >= 0; j-- {
				switch blocks[j].(type) {
				case *types.ContentBlockMemberCachePoint:
					return nil
				case *types.ContentBlockMemberToolUse, *types.ContentBlockMemberToolResult, *types.ContentBlockMemberImage:
					target = j
				}
				if target >= 0 {
					break
				}
			}
		}
		if target >= 0 {
			content := make([]types.ContentBlock, 0, len(blocks)+1)
			content = append(content, blocks[:target+1]...)
			content = append(content, &types.ContentBlockMemberCachePoint{Value: point})
			input.Messages[i].Content = append(content, blocks[target+1:]...)
			return nil
		}
	}
	return nil
}
