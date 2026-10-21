package provider

import (
	"net/http"

	modelcfg "vista/internal/model"
)

func aihubmixHeader(request *http.Request, model modelcfg.Model) {
	if model.ProviderID == "aihubmix" {
		code := model.AppCode
		if code == "" {
			code = "WHVL9885"
		}
		request.Header.Set("APP-Code", code)
	}
}
