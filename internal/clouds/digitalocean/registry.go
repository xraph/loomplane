package digitalocean

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const RegistryHost = "registry.digitalocean.com"
const RegistryAuthEndpoint = "https://api.digitalocean.com/v2/registry/auth"

var registryName = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

func (c *Client) RegistryNames(ctx context.Context, token string) ([]string, error) {
	var list struct {
		Registries []struct {
			Name string `json:"name"`
		} `json:"registries"`
	}
	// A DigitalOcean team has at most ten registries.
	if err := c.do(ctx, token, http.MethodGet, "/v2/registries", url.Values{"per_page": {"200"}}, nil, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Registries))
	for _, r := range list.Registries {
		names = append(names, r.Name)
	}
	return names, nil
}

func (c *Client) RegistryLogin(ctx context.Context, token, name string) (user, password string, err error) {
	if !registryName.MatchString(name) {
		return "", "", errors.New("invalid DigitalOcean registry name")
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if loginErr := c.do(ctx, token, http.MethodGet, "/v2/registries/"+name+"/docker-credentials", url.Values{"read_write": {"true"}, "expiry_seconds": {"3600"}}, nil, &config); loginErr != nil {
		return "", "", loginErr
	}
	raw, err := base64.StdEncoding.DecodeString(config.Auths[RegistryHost].Auth)
	user, password, ok := strings.Cut(string(raw), ":")
	if err != nil || !ok || user == "" || password == "" {
		return "", "", errors.New("DigitalOcean did not return Docker credentials for its registry")
	}
	return user, password, nil
}
