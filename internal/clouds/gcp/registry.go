package gcp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// artifactLocation is an Artifact Registry location, such as us-central1
// or europe.
var artifactLocation = regexp.MustCompile(`^[a-z]+(?:-[a-z]+\d*)?$`)

// RegistryEndpoint is Artifact Registry's Docker endpoint in location.
func RegistryEndpoint(location string) (string, error) {
	if !artifactLocation.MatchString(location) {
		return "", fmt.Errorf("%q isn't an Artifact Registry location", location)
	}
	return "https://" + location + "-docker.pkg.dev", nil
}

// RegistryLogin is a Docker login for Artifact Registry: the session's
// short-lived access token, as Google's own credential helper uses it. It
// carries the service account's permissions, so give the connection a
// service account that may only write the repository it builds into.
func (s *Session) RegistryLogin(_ context.Context) (user, password string, err error) {
	tok, err := s.ts.Token()
	if err != nil {
		return "", "", err
	}
	if tok.AccessToken == "" {
		return "", "", errors.New("Google returned no access token") //nolint:staticcheck // a product name
	}
	return "oauth2accesstoken", tok.AccessToken, nil
}
