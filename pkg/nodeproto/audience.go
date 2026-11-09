package nodeproto

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ErrAudienceURL is returned for an audience that is not an http or https URL.
var ErrAudienceURL = errors.New("nodeproto: the audience must be an http or https URL with a host")

// NormalizeAudience puts an instance URL in the one form both sides sign and
// compare: scheme and host lowercased, no default port, no trailing slash.
// It refuses an empty value, user info, a query or a fragment.
func NormalizeAudience(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrAudienceURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrAudienceURL
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", ErrAudienceURL
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if host == "" {
		return "", ErrAudienceURL
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}
