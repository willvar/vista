package provider

import (
	"context"
	"strings"

	modelcfg "vista/internal/model"
)

// Catalog adapters own service-specific rules. Unlisted services use the pinned
// protocol, URL and credential names without requiring a catalog-layer branch.
type catalogAdapter struct {
	resolveRoute       func(*catalogResolveState) error
	resolveEndpoint    func(*catalogResolveState) error
	resolveCredential  func(*catalogResolveState) error
	finishEndpoint     func(*catalogResolveState) error
	finish             func(*catalogResolveState) error
	prepareRun         func(context.Context, modelcfg.Model, string) (modelcfg.Model, string, error)
	prepareRequest     func(*modelcfg.Model, RequestPurpose)
	validation         *catalogValidationPolicy
	ambientCredentials bool
}

var catalogAdapters = map[string]catalogAdapter{
	"deepinfra":                {finish: finishDeepinfraEndpoint},
	"":                         {validation: &openaiValidation},
	"openai":                   {validation: &openaiValidation},
	"openai-codex":             {resolveCredential: func(*catalogResolveState) error { return nil }, validation: &openaiValidation, ambientCredentials: true},
	"anthropic":                {resolveCredential: resolveAnthropicCredential, validation: &anthropicValidation},
	"zai":                      {validation: &zaiValidation},
	"zhipuai":                  {validation: &zaiValidation},
	"zai-coding-plan":          {resolveRoute: resolveZAIRoute, validation: &zaiValidation},
	"zhipuai-coding-plan":      {resolveRoute: resolveZAIRoute, validation: &zaiValidation},
	"moonshotai":               {resolveRoute: resolveMoonshotRoute, validation: &moonshotValidation},
	"moonshotai-cn":            {resolveRoute: resolveMoonshotRoute, validation: &moonshotValidation},
	"alibaba":                  {resolveRoute: resolveAlibabaRoute, validation: &alibabaValidation},
	"alibaba-cn":               {resolveRoute: resolveAlibabaRoute, validation: &alibabaValidation},
	"meta":                     {resolveRoute: resolveMetaRoute, validation: &metaValidation},
	"xai":                      {resolveRoute: resolveXAIRoute, validation: &xaiValidation},
	"gitlab":                   {resolveEndpoint: resolveGitLabEndpoint, prepareRun: resolveGitLabForRun},
	"google-vertex":            {resolveEndpoint: resolveVertexEndpoint, resolveCredential: resolveVertexCredential, prepareRun: resolveVertexForRun, ambientCredentials: true},
	"amazon-bedrock":           {resolveEndpoint: resolveBedrockEndpoint, resolveCredential: resolveBedrockCredential, validation: &bedrockValidation},
	"aihubmix":                 {resolveEndpoint: resolveAihubmixEndpoint},
	"azure":                    {resolveEndpoint: resolveAzureEndpoint, validation: &openaiValidation},
	"azure-cognitive-services": {resolveEndpoint: resolveAzureEndpoint, validation: &openaiValidation},
	"neon":                     {resolveEndpoint: resolveNeonEndpoint},
	"qvac":                     {resolveEndpoint: resolveQvacEndpoint, resolveCredential: resolveQvacCredential},
	"salad-cloud":              {resolveEndpoint: resolveSaladEndpoint},
	"watsonx":                  {resolveEndpoint: resolveWatsonxEndpoint, prepareRun: resolveWatsonxForRun},
	"sap-ai-core":              {resolveEndpoint: resolveSAPEndpoint, prepareRun: resolveSAPAIForRun},
	"databricks":               {resolveEndpoint: resolveHostEndpoint},
	"snowflake-cortex":         {resolveEndpoint: resolveHostEndpoint, resolveCredential: resolveSnowflakeCredential},
	"infomaniak":               {resolveEndpoint: resolveInfomaniakEndpoint},
	"cloudflare-ai-gateway":    {finishEndpoint: resolveCloudflareEndpoint, finish: finishCloudflareEndpoint},
	"cloudflare-workers-ai":    {finishEndpoint: resolveCloudflareEndpoint},
	"github-copilot":           {finish: finishCopilotEndpoint, prepareRequest: prepareCopilotRequest},
}

func catalogAdapterFor(id string) catalogAdapter {
	if adapter, ok := catalogAdapters[id]; ok {
		return adapter
	}
	if strings.HasPrefix(id, "minimax") {
		return catalogAdapter{resolveRoute: resolveMiniMaxRoute, validation: &minimaxValidation}
	}
	if strings.HasPrefix(id, "alibaba") {
		return catalogAdapter{validation: &alibabaValidation}
	}
	return catalogAdapter{}
}
