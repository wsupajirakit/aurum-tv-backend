package stream

import (
	"net/url"
	"strings"
)

// ValidateSyncURL accepts only https .m3u8 links on allowed hosts (suffix match), so nobody can
// point every TV's stream at their own server. It returns the cleaned url or a reason code.
func ValidateSyncURL(raw string, allowedHosts []string) (string, string) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "missing_url"
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", "invalid_url"
	}
	if u.Scheme != "https" {
		return "", "https_required"
	}
	if !strings.HasSuffix(u.Path, ".m3u8") {
		return "", "not_m3u8"
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowedHosts {
		allowed = strings.ToLower(allowed)
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return value, ""
		}
	}
	return "", "host_not_allowed"
}
