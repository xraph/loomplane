package federation

import (
	"net/url"
	"regexp"
	"strings"
)

var pinnedAgentImage = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

// ValidAgentImage requires an immutable SHA-256 image reference, never a URL.
func ValidAgentImage(image string) bool {
	return pinnedAgentImage.MatchString(image) && !strings.Contains(image, "//")
}

// ValidControlEndpoint accepts an HTTPS origin without credentials or routing
// suffixes. Management configuration and agent transport share this boundary.
func ValidControlEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawPath == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == ""
}
