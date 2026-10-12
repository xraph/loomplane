package gcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SecretAccessor is the role a service's runtime account needs to read a
// secret.
const SecretAccessor = "roles/secretmanager.secretAccessor"

// EnsureSecret creates a secret, if it isn't there, replicated where
// Google chooses.
func (s *Session) EnsureSecret(ctx context.Context, id string) error {
	u := s.c.url("secretmanager.googleapis.com", "/v1/projects/"+s.key.ProjectID+"/secrets?secretId="+url.QueryEscape(id))
	err := s.do(ctx, http.MethodPost, u, map[string]any{
		"replication": map[string]any{"automatic": map[string]any{}},
		"labels":      map[string]string{"managed-by": "loomplane"},
	}, nil)
	if IsConflict(err) {
		return nil
	}
	return err
}

// AddSecretVersion stores value as a new version of a secret and returns
// the version's number.
func (s *Session) AddSecretVersion(ctx context.Context, id string, value []byte) (string, error) {
	var out struct {
		Name string `json:"name"`
	}
	u := s.c.url("secretmanager.googleapis.com", "/v1/projects/"+s.key.ProjectID+"/secrets/"+url.PathEscape(id)+":addVersion")
	if err := s.do(ctx, http.MethodPost, u, map[string]any{
		"payload": map[string]string{"data": base64.StdEncoding.EncodeToString(value)},
	}, &out); err != nil {
		return "", err
	}
	i := strings.LastIndex(out.Name, "/versions/")
	if i < 0 {
		return "", fmt.Errorf("secret manager named no version: %q", out.Name)
	}
	return out.Name[i+len("/versions/"):], nil
}

// AllowSecret lets one service account read a secret, and nobody else
// through the secret's own policy.
func (s *Session) AllowSecret(ctx context.Context, id, member string) error {
	u := s.c.url("secretmanager.googleapis.com", "/v1/projects/"+s.key.ProjectID+"/secrets/"+url.PathEscape(id)+":setIamPolicy")
	return s.do(ctx, http.MethodPost, u, map[string]any{
		"policy": map[string]any{"bindings": []map[string]any{{"role": SecretAccessor, "members": []string{member}}}},
	}, nil)
}

// RuntimeAccount is the service account Cloud Run services run as when
// none is set: the project's default compute account.
func (s *Session) RuntimeAccount(ctx context.Context) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := s.do(ctx, http.MethodGet, s.c.url("cloudresourcemanager.googleapis.com", "/v3/projects/"+s.key.ProjectID), nil, &p); err != nil {
		return "", err
	}
	number := strings.TrimPrefix(p.Name, "projects/")
	if number == "" || number == p.Name {
		return "", fmt.Errorf("resource manager named no project number: %q", p.Name)
	}
	return "serviceAccount:" + number + "-compute@developer.gserviceaccount.com", nil
}
