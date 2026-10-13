// Package registry checks explicit OCI registry connections.
package registry

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalid        = errors.New("invalid registry connection")
	ErrRejected       = errors.New("the registry refused the credential")
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
)

// Config separates encrypted authentication from safe connection metadata.
type Config struct {
	Endpoint, Repository, AuthEndpoint string
	Username, Password, Token          string
	TLS                                *tls.Config
	Details                            map[string]string
}

func safeURL(raw string, endpoint bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (endpoint && u.Path != "" && u.Path != "/") {
		return nil, ErrInvalid
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("%w: HTTPS is required outside loopback", ErrInvalid)
	}
	return u, nil
}

// ParseConfig accepts JSON credentials. An empty object selects anonymous access.
func ParseConfig(raw []byte, settings map[string]string) (*Config, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, fmt.Errorf("%w: provide credential JSON, or {} for anonymous access", ErrInvalid)
	}
	var auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
		CA       string `json:"ca"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, fmt.Errorf("%w: malformed credential JSON", ErrInvalid)
	}
	if (auth.Username == "") != (auth.Password == "") || (auth.Token != "" && auth.Username != "") || strings.ContainsAny(auth.Username+auth.Token, "\r\n") {
		return nil, fmt.Errorf("%w: use username/password or a bearer token", ErrInvalid)
	}
	u, err := safeURL(strings.TrimSpace(settings["endpoint"]), true)
	if err != nil {
		return nil, err
	}
	repository := strings.TrimSpace(settings["repository"])
	if len(repository) > 200 || !repositoryPattern.MatchString(repository) {
		return nil, fmt.Errorf("%w: provide a lowercase repository prefix", ErrInvalid)
	}
	cfg := &Config{Endpoint: strings.TrimSuffix(u.String(), "/"), Repository: repository, Username: auth.Username, Password: auth.Password, Token: auth.Token, TLS: &tls.Config{MinVersion: tls.VersionTLS12}}
	if auth.CA != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(auth.CA)) {
			return nil, fmt.Errorf("%w: invalid CA certificate", ErrInvalid)
		}
		cfg.TLS.RootCAs = pool
	}
	if realm := strings.TrimSpace(settings["auth_endpoint"]); realm != "" {
		approved, err := safeURL(realm, false)
		if err != nil {
			return nil, err
		}
		cfg.AuthEndpoint = approved.String()
	}
	cfg.Details = map[string]string{"endpoint": cfg.Endpoint, "repository": repository}
	if cfg.AuthEndpoint != "" {
		cfg.Details["auth_endpoint"] = cfg.AuthEndpoint
	}
	if auth.CA != "" {
		cfg.Details["registry_ca"] = fmt.Sprintf("%x", sha256.Sum256([]byte(auth.CA)))
	}
	return cfg, nil
}

// New creates a bounded client that never follows credential-bearing redirects.
func New(cfg *Config) *Client {
	return &Client{Config: cfg, HTTP: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg.TLS, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
