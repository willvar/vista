package modelcatalog

import "vista/internal/codexauth"

func bindCodexCredentials(dir string, catalog Catalog) Catalog {
	for index := range catalog.Models {
		model := &catalog.Models[index]
		if model.ProviderID == "openai-codex" && model.APIKey == "" {
			model.codexCredentialPath = codexauth.Path(dir, model.ConnectionID)
		}
	}
	return catalog
}
