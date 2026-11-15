package provider

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"golang.org/x/oauth2/google/externalaccount"
)

// Inspect source descriptors only; google.CredentialsFromJSON still owns ADC
// validation, precedence, signing, token exchange, impersonation and caching.
func vertexRuntimeIdentityRequests(data []byte) (func(*http.Request) bool, error) {
	type descriptor struct {
		Type              string                           `json:"type"`
		CredentialSource  externalaccount.CredentialSource `json:"credential_source"`
		SourceCredentials json.RawMessage                  `json:"source_credentials"`
	}
	var endpoints []*url.URL
	for len(data) > 0 {
		var source descriptor
		if err := json.Unmarshal(data, &source); err != nil {
			return nil, err
		}
		if source.Type == "impersonated_service_account" {
			data = source.SourceCredentials
			continue
		}
		var sources []string
		if source.Type == "external_account" && strings.HasPrefix(source.CredentialSource.EnvironmentID, "aws") {
			// AWS federation retrieves these configured metadata URLs, including
			// the role name appended to URL. The signed STS URL is not fetched.
			sources = []string{source.CredentialSource.URL, source.CredentialSource.RegionURL, source.CredentialSource.IMDSv2SessionTokenURL}
		} else if source.Type == "external_account" {
			// Metadata headers also identify custom Azure/GCE source endpoints.
			for key, value := range source.CredentialSource.Headers {
				if strings.EqualFold(key, "Metadata-Flavor") && value == "Google" || strings.EqualFold(key, "Metadata") && strings.EqualFold(value, "true") {
					sources = []string{source.CredentialSource.URL}
					break
				}
			}
		}
		for _, raw := range sources {
			if raw == "" {
				continue
			}
			endpoint, err := url.Parse(raw)
			if err != nil {
				return nil, err
			}
			endpoints = append(endpoints, endpoint)
		}
		break
	}
	gceHost := os.Getenv("GCE_METADATA_HOST")
	return func(r *http.Request) bool {
		// URL-sourced Azure/GCE identity uses the standard metadata authority.
		// Arbitrary remote URL token suppliers retain the connection's route.
		host := strings.ToLower(strings.TrimSuffix(r.URL.Hostname(), "."))
		if address, err := netip.ParseAddr(host); err == nil {
			host = address.Unmap().String()
		}
		switch host {
		case "169.254.169.254", "169.254.170.2", "169.254.170.23", "fd00:ec2::254", "fd00:ec2::23", "metadata.google.internal", "metadata.google":
			return true
		}
		if gceHost != "" && strings.EqualFold(r.URL.Host, gceHost) {
			return true
		}
		for _, endpoint := range endpoints {
			if strings.EqualFold(r.URL.Scheme, endpoint.Scheme) && strings.EqualFold(r.URL.Host, endpoint.Host) &&
				(r.URL.Path == endpoint.Path || strings.HasPrefix(r.URL.Path, strings.TrimSuffix(endpoint.Path, "/")+"/")) {
				return true
			}
		}
		return false
	}, nil
}
