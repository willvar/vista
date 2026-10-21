package provider

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

func resolveGitLabEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "gitlab" {
		if model.InstanceURL == "" {
			model.InstanceURL = os.Getenv("GITLAB_INSTANCE_URL")
		}
		if model.InstanceURL == "" {
			model.InstanceURL = "https://gitlab.com"
		}
		if !ValidProviderBaseURL(model.InstanceURL) {
			return fmt.Errorf("catalog model %q needs a valid GitLab instanceUrl", model.Key)
		}
		if model.BaseURL == "" {
			gateway := os.Getenv("GITLAB_AI_GATEWAY_URL")
			if gateway == "" {
				gateway = "https://cloud.gitlab.com"
			}
			if !ValidProviderBaseURL(gateway) {
				return fmt.Errorf("catalog model %q needs a valid GITLAB_AI_GATEWAY_URL", model.Key)
			}
			gateway = strings.TrimRight(gateway, "/") + "/ai/v1/proxy"
			if model.Provider == "anthropic" {
				model.BaseURL = gateway + "/anthropic"
			} else {
				model.BaseURL = gateway + "/openai/v1"
			}
		}
	}

	return nil
}

func resolveVertexEndpoint(state *catalogResolveState) error {
	model := &state.config
	entry := state.entry
	if model.ProviderID == "google-vertex" {
		if model.AccessToken != "" && model.APIKey != "" {
			return fmt.Errorf("catalog model %q cannot combine Google Vertex apiKey and accessToken", model.Key)
		}
		if model.Provider != "vertex-gemini" && model.APIKey != "" {
			return fmt.Errorf("catalog model %q does not support a Google Vertex API key", model.Key)
		}
		if model.Provider == "vertex-gemini" && model.APIKey == "" && model.AccessToken == "" {
			if key := os.Getenv("GOOGLE_VERTEX_API_KEY"); key != "" {
				model.APIKey = key
				state.credentialEnv = append(state.credentialEnv, "GOOGLE_VERTEX_API_KEY")
			}
		}
		if model.Project == "" {
			for _, name := range []string{"GOOGLE_VERTEX_PROJECT", "GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT"} {
				if model.Project = os.Getenv(name); model.Project != "" {
					break
				}
			}
		}
		if model.Location == "" {
			for _, name := range []string{"GOOGLE_VERTEX_LOCATION", "GOOGLE_CLOUD_LOCATION", "VERTEX_LOCATION"} {
				if model.Location = os.Getenv(name); model.Location != "" {
					break
				}
			}
		}
		if model.Location == "" {
			model.Location = "global"
			if model.Provider == "vertex-gemini" {
				model.Location = "us-central1"
			}
		}
		if model.Project != "" && !validCatalogPathSegment(model.Project) || !validCatalogPathSegment(model.Location) || model.Project == "" && model.BaseURL == "" && model.APIKey == "" {
			return fmt.Errorf("catalog model %q needs a valid Google Vertex project and location", model.Key)
		}
		if model.BaseURL == "" {
			host := os.Getenv("GOOGLE_VERTEX_ENDPOINT")
			if host == "" {
				host = "aiplatform.googleapis.com"
				if entry.Package != "@ai-sdk/openai-compatible" && model.APIKey == "" {
					switch model.Location {
					case "global":
					case "us", "eu":
						host = "aiplatform." + model.Location + ".rep.googleapis.com"
					default:
						host = model.Location + "-aiplatform.googleapis.com"
					}
				}
			}
			if !validCatalogHost(host) {
				return fmt.Errorf("catalog model %q needs a valid GOOGLE_VERTEX_ENDPOINT", model.Key)
			}
			root := "https://" + host + "/v1/projects/" + model.Project + "/locations/" + model.Location
			switch entry.Package {
			case "@ai-sdk/google-vertex":
				if model.APIKey != "" {
					model.BaseURL = "https://" + host + "/v1/publishers/google"
				} else {
					model.BaseURL = "https://" + host + "/v1beta1/projects/" + model.Project + "/locations/" + model.Location + "/publishers/google"
				}
			case "@ai-sdk/google-vertex/anthropic":
				model.BaseURL = root + "/publishers/anthropic"
			case "@ai-sdk/openai-compatible":
				model.BaseURL = root + "/endpoints/openapi"
			default:
				return fmt.Errorf("catalog model %q has unsupported Google Vertex package", model.Key)
			}
		}
	}

	return nil
}

func resolveBedrockEndpoint(state *catalogResolveState) error {
	model := &state.config
	entry := state.entry
	if model.ProviderID == "amazon-bedrock" && model.Region == "" {
		model.Region = os.Getenv("AWS_REGION")
		if model.Region == "" {
			model.Region = os.Getenv("AWS_DEFAULT_REGION")
		}
		if model.Region == "" {
			model.Region = "us-east-1"
		}
	}
	if model.BaseURL == "" && strings.Contains(entry.BaseURL, "${AWS_REGION}") {
		model.BaseURL = strings.ReplaceAll(entry.BaseURL, "${AWS_REGION}", model.Region)
	}

	return nil
}

func resolveAihubmixEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "aihubmix" && model.AppCode == "" {
		model.AppCode = "WHVL9885"
	}

	return nil
}

func resolveAzureEndpoint(state *catalogResolveState) error {
	model := &state.config
	entry := state.entry
	state.metadata.AzureCatalog = model.ProviderID == "azure" || model.ProviderID == "azure-cognitive-services"
	state.metadata.AzureNative = entry.Package == "@ai-sdk/azure"
	if state.metadata.AzureNative && model.DeploymentID != "" {
		state.metadata.WireID = model.DeploymentID
	}
	if state.metadata.AzureNative && model.UseCompletionURLs != nil && *model.UseCompletionURLs {
		model.Provider = "openai-chat"
	}
	if state.metadata.AzureCatalog && model.BaseURL == "" {
		resource := model.ResourceName
		if resource == "" {
			resource = os.Getenv(azureResourceEnvName(model.ProviderID))
		}
		if state.metadata.AzureNative || strings.Contains(entry.BaseURL, "${"+azureResourceEnvName(model.ProviderID)+"}") {
			if err := validateAzureResourceName(resource); err != nil {
				return fmt.Errorf("catalog model %q: %w", model.Key, err)
			}
			if state.metadata.AzureNative {
				model.BaseURL = "https://" + resource + ".openai.azure.com/openai"
				if model.UseDeploymentURLs == nil || !*model.UseDeploymentURLs {
					model.BaseURL += "/v1"
				}
			} else {
				model.BaseURL = strings.ReplaceAll(entry.BaseURL, "${"+azureResourceEnvName(model.ProviderID)+"}", resource)
			}
		}
	}

	return nil
}

func resolveNeonEndpoint(state *catalogResolveState) error {
	model := &state.config
	entry := state.entry
	if model.ProviderID == "neon" && model.BaseURL == "" {
		base := strings.TrimRight(os.Getenv("NEON_AI_GATEWAY_BASE_URL"), "/")
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && localLoopback(u.Hostname()))) {
			return fmt.Errorf("catalog model %q needs a valid NEON_AI_GATEWAY_BASE_URL or baseUrl", model.Key)
		}
		model.BaseURL = strings.Replace(entry.BaseURL, "${NEON_AI_GATEWAY_BASE_URL}", base, 1)
	}

	return nil
}

func resolveQvacEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "qvac" && model.BaseURL == "" {
		model.BaseURL = os.Getenv("QVAC_BASE_URL")
		if model.BaseURL == "" {
			return fmt.Errorf("catalog model %q needs baseUrl or QVAC_BASE_URL for a running qvac serve --openai endpoint", model.Key)
		}
	}

	return nil
}

func resolveSaladEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "salad-cloud" && model.BaseURL == "" {
		if base := os.Getenv("SALAD_CLOUD_BASE_URL"); base != "" {
			model.BaseURL = base
		}
	}

	return nil
}

func resolveWatsonxEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "watsonx" && model.ProjectID == "" {
		model.ProjectID = os.Getenv("WATSONX_AI_PROJECT_ID")
	}
	if model.ProviderID == "watsonx" && model.APIKey != "" {
		state.credentialEnv = append(state.credentialEnv, "WATSONX_AI_APIKEY")
	}

	return nil
}

func resolveSAPEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "sap-ai-core" {
		if model.APIKey == "" {
			model.APIKey = os.Getenv("AICORE_SERVICE_KEY")
		}
		state.credentialEnv = append(state.credentialEnv, "AICORE_SERVICE_KEY")
		if model.DeploymentID == "" {
			model.DeploymentID = os.Getenv("AICORE_DEPLOYMENT_ID")
		}
		if model.ResourceGroup == "" {
			model.ResourceGroup = os.Getenv("AICORE_RESOURCE_GROUP")
		}
		if model.ResourceGroup == "" {
			model.ResourceGroup = "default"
		}
	}

	return nil
}

func resolveHostEndpoint(state *catalogResolveState) error {
	model := &state.config
	entry := state.entry
	if model.BaseURL == "" {
		name := catalogHostVariable(model.ProviderID)
		host := os.Getenv(name)
		if !validCatalogHost(host) {
			return fmt.Errorf("catalog model %q needs a valid %s or baseUrl", model.Key, name)
		}
		model.BaseURL = strings.Replace(entry.BaseURL, "${"+name+"}", host, 1)
	}

	return nil
}

func resolveInfomaniakEndpoint(state *catalogResolveState) error {
	model := &state.config
	entry := state.entry
	if model.BaseURL == "" {
		product := os.Getenv("INFOMANIAK_PRODUCT_ID")
		if !validCatalogPathSegment(product) {
			return fmt.Errorf("catalog model %q needs a valid INFOMANIAK_PRODUCT_ID or baseUrl", model.Key)
		}
		model.BaseURL = strings.Replace(entry.BaseURL, "${INFOMANIAK_PRODUCT_ID}", product, 1)
	}

	return nil
}

func resolveCloudflareEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.BaseURL == "" {
		if model.ProviderID == "cloudflare-ai-gateway" || model.ProviderID == "cloudflare-workers-ai" {
			account := model.AccountID
			if account == "" {
				account = os.Getenv("CLOUDFLARE_ACCOUNT_ID")
			}
			if !validCloudflareAccountID(account) {
				return fmt.Errorf("catalog model %q needs a valid Cloudflare accountId or baseUrl", model.Key)
			}
			model.BaseURL = "https://api.cloudflare.com/client/v4/accounts/" + account + "/ai/v1"
		}
	}

	return nil
}

func finishCopilotEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "github-copilot" && model.Provider == "anthropic" {
		model.BaseURL = strings.TrimRight(model.BaseURL, "/")
		if !strings.HasSuffix(model.BaseURL, "/v1") {
			model.BaseURL += "/v1"
		}
	}

	return nil
}

func finishCloudflareEndpoint(state *catalogResolveState) error {
	model := &state.config
	if model.ProviderID == "cloudflare-ai-gateway" && model.GatewayID == "" {
		model.GatewayID = os.Getenv("CLOUDFLARE_GATEWAY_ID")
	}
	return nil
}

func finishDeepinfraEndpoint(state *catalogResolveState) error {
	if state.metadata.BaseURLExplicit {
		state.config.BaseURL = strings.TrimRight(state.config.BaseURL, "/")
		if !strings.HasSuffix(state.config.BaseURL, "/openai") {
			state.config.BaseURL += "/openai"
		}
	}
	return nil
}
