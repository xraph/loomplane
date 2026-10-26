package dbgateway

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/deployments"
)

// versionSecrets keeps one version per key and reads nothing else.
type versionSecrets map[string]string

func (versionSecrets) Put(context.Context, string, []byte) error { return nil }
func (versionSecrets) Delete(context.Context, string) error      { return nil }
func (versionSecrets) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("Get called")
}

func (v versionSecrets) Version(_ context.Context, key string) (string, error) {
	s, ok := v[key]
	if !ok {
		return "", deployments.ErrNoCredential
	}
	return s, nil
}

// getOnly keeps no versions.
type getOnly struct{}

func (getOnly) Put(context.Context, string, []byte) error   { return nil }
func (getOnly) Delete(context.Context, string) error        { return nil }
func (getOnly) Get(context.Context, string) ([]byte, error) { return nil, nil }

func TestResourceURLVersions(t *testing.T) {
	ctx := t.Context()
	u := resourceURLs{secrets: versionSecrets{deployments.ResourceURLKey("org_1", "env_1", "orders"): "v3"}}
	if v, err := u.URLVersion(ctx, "org_1", "env_1", "orders"); err != nil || v != "v3" {
		t.Fatalf("URLVersion = %q, %v; want v3", v, err)
	}
	if _, err := u.URLVersion(ctx, "org_1", "env_1", "billing"); !errors.Is(err, dbgateway.ErrNoURL) {
		t.Fatalf("URLVersion of a missing address = %v, want ErrNoURL", err)
	}
	if _, err := (resourceURLs{secrets: getOnly{}}).URLVersion(ctx, "org_1", "env_1", "orders"); err == nil {
		t.Fatal("URLVersion over secrets that keep no versions succeeded")
	}
}
