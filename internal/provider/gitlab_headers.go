package provider

import (
	"net/http"

	modelcfg "vista/internal/model"
)

func gitlabHeaders(request *http.Request, model modelcfg.Model) {
	if model.ProviderID != "gitlab" {
		return
	}
	for name, value := range model.GitLabHeaders {
		request.Header.Set(name, value)
	}
}
