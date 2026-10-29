package config

import (
	"errors"
	"net/url"
)

// DefaultPushGatewayURL is XRaph's gateway, the only one that can reach the
// store app.
const DefaultPushGatewayURL = "https://push.loomplane.com"

// PushConfig is how this server hands phone notifications to a gateway.
type PushConfig struct {
	// Enabled turns push on. Off, the app hides push settings for this
	// server and every push route but config answers 404.
	Enabled bool
	// GatewayURL is the gateway that hands notifications to Apple and Google.
	GatewayURL string
}

// Validate refuses a gateway address that isn't a plain https origin (http
// in development).
func (p PushConfig) Validate(environment string) error {
	if !p.Enabled {
		return nil
	}
	u, err := url.Parse(p.GatewayURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("push.gateway_url must be an absolute URL")
	}
	if u.User != nil {
		return errors.New("push.gateway_url can't carry user info")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("push.gateway_url can't carry a query or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || environment != EnvDevelopment) {
		return errors.New("push.gateway_url must use https (http is allowed only in development)")
	}
	return nil
}
