package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// DefaultHuggingFaceURL is the Hugging Face Hub the models section reads
// from unless ai.models.huggingface_url points it somewhere else.
const DefaultHuggingFaceURL = "https://huggingface.co"

// AIConfig switches the AI section on. It is off unless the operator asks.
type AIConfig struct {
	// Enabled turns on the AI section (ai.enabled). Later AI settings join
	// this struct rather than starting a second block.
	Enabled bool
	// Models configures where models come from (ai.models).
	Models AIModelsConfig
}

// AIModelsConfig configures the model registry's outside sources.
type AIModelsConfig struct {
	// HuggingFaceURL is the Hugging Face Hub, or a mirror of it
	// (ai.models.huggingface_url). The workspace's Hugging Face token goes
	// to this host, so it must be https; plain http is allowed only for a
	// loopback host, for tests and a mirror on the same machine.
	HuggingFaceURL string
}

// Validate refuses a Hugging Face URL the token shouldn't be sent to. An
// empty one is the default.
func (c AIModelsConfig) Validate() error {
	if c.HuggingFaceURL == "" {
		return nil
	}
	u, err := url.Parse(c.HuggingFaceURL)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("ai.models.huggingface_url must be an absolute URL like %s, got %q", DefaultHuggingFaceURL, c.HuggingFaceURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("ai.models.huggingface_url can't carry a user, a query or a fragment, got %q", c.HuggingFaceURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("ai.models.huggingface_url must be https, since the workspace's Hugging Face token is sent to it (http only for localhost), got %q", c.HuggingFaceURL)
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
