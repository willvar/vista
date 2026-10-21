package provider

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// The pinned Provider env list also contains resource names, endpoint URLs,
// projects, and account IDs. Only credential-shaped names may become API keys.
func catalogCredentialName(name string) bool {
	for _, suffix := range []string{"_API_KEY", "_APIKEY", "_KEY", "_TOKEN", "_SECRET", "_PAT"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func validAWSRegion(region string) bool {
	if len(region) < 5 || len(region) > 32 || region[0] == '-' || region[len(region)-1] == '-' || !strings.Contains(region, "-") {
		return false
	}
	for _, ch := range region {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

func validateAzureResourceName(name string) error {
	if name == "" || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return errors.New("Azure native model needs a valid resourceName or baseUrl")
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return errors.New("Azure resourceName contains invalid characters")
		}
	}
	return nil
}

func validCloudflareAccountID(account string) bool {
	if len(account) == 0 || len(account) > 128 {
		return false
	}
	for _, ch := range account {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func catalogHostVariable(providerID string) string {
	switch providerID {
	case "databricks":
		return "DATABRICKS_HOST"
	case "snowflake-cortex":
		return "SNOWFLAKE_ACCOUNT"
	default:
		return ""
	}
}

func validCatalogHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func validCatalogPathSegment(segment string) bool {
	if len(segment) == 0 || len(segment) > 64 {
		return false
	}
	for _, ch := range segment {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func validAlibabaHostSegment(segment string) bool {
	return validCatalogPathSegment(segment) && !strings.Contains(segment, "_") && segment[0] != '-' && segment[len(segment)-1] != '-'
}

func ValidProviderBaseURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http" && localLoopback(u.Hostname()))
}

func azureResourceEnvName(providerID string) string {
	if providerID == "azure-cognitive-services" {
		return "AZURE_COGNITIVE_SERVICES_RESOURCE_NAME"
	}
	return "AZURE_RESOURCE_NAME"
}

func localLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
