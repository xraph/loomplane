package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
)

func TestExistingAPIAccessRotationAndDetachRemoveOldSecrets(t *testing.T) {
	secrets := snapshotSecrets{}
	r := NewRunner(RunnerDeps{Resources: secrets})
	d := &Deployment{OrgID: "org", EnvironmentID: "env"}
	a := planning.Action{Name: "db", Resource: &planning.ResourceConfig{Kind: "cloudflare-d1"}}
	account := strings.Repeat("a", 32)
	record := &providerresources.ProviderResource{Provider: "cloudflare", Kind: "cloudflare-d1", ExternalID: "database-id", Incarnation: "database-id", Name: "database", Scope: "accounts/" + account + "/d1"}
	endpoint := &catalogconnect.Endpoint{Engine: "cloudflare-d1"}
	access := catalogconnect.APIAccess{Provider: "cloudflare", Kind: "cloudflare-d1", AccountID: account, ResourceID: record.ExternalID, Token: "application-secret"}
	raw, err := catalogconnect.EncodeAPIAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.storeAPIExistingAccess(t.Context(), d, a, record, endpoint, raw); err != nil {
		t.Fatal(err)
	}
	key := resourceKey("org", "env", "db", "output:CLOUDFLARE_API_TOKEN")
	if string(secrets[key]) != "application-secret" {
		t.Fatal("application access not captured")
	}
	access.Token = ""
	access.Native = true
	raw, err = catalogconnect.EncodeAPIAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.storeAPIExistingAccess(t.Context(), d, a, record, endpoint, raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets[key]; ok {
		t.Fatal("rotation retained obsolete API token")
	}
	if err = r.clearExistingAccess(t.Context(), d, "db"); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 0 {
		t.Fatal("detachment retained application outputs")
	}
}

type interruptedAPIAccessSecrets struct {
	snapshotSecrets
	fail string
}

func (s *interruptedAPIAccessSecrets) Put(ctx context.Context, key string, value []byte) error {
	if key == s.fail {
		return errors.New("injected storage failure")
	}
	return s.snapshotSecrets.Put(ctx, key, value)
}

func TestInterruptedFirstAPIAccessRemainsTrackedForNativeRetry(t *testing.T) {
	account := strings.Repeat("a", 32)
	record := &providerresources.ProviderResource{Provider: "cloudflare", Kind: "cloudflare-d1", ExternalID: "database-id", Incarnation: "database-id", Name: "database", Scope: "accounts/" + account + "/d1"}
	endpoint := &catalogconnect.Endpoint{Engine: "cloudflare-d1"}
	access := catalogconnect.APIAccess{Provider: "cloudflare", Kind: "cloudflare-d1", AccountID: account, ResourceID: record.ExternalID, Token: "application-secret"}
	raw, err := catalogconnect.EncodeAPIAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	store := &interruptedAPIAccessSecrets{snapshotSecrets: snapshotSecrets{}, fail: resourceKey("org", "env", "db", "output:CLOUDFLARE_BINDING")}
	r := NewRunner(RunnerDeps{Resources: store})
	d := &Deployment{OrgID: "org", EnvironmentID: "env"}
	a := planning.Action{Name: "db", Resource: &planning.ResourceConfig{Kind: "cloudflare-d1"}}
	if err = r.storeAPIExistingAccess(t.Context(), d, a, record, endpoint, raw); err == nil {
		t.Fatal("fixture did not interrupt credential writes")
	}
	store.fail = ""
	access.Native = true
	access.Token = ""
	raw, err = catalogconnect.EncodeAPIAccess(access)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.storeAPIExistingAccess(t.Context(), d, a, record, endpoint, raw); err != nil {
		t.Fatal(err)
	}
	if _, present := store.snapshotSecrets[resourceKey("org", "env", "db", "output:CLOUDFLARE_API_TOKEN")]; present {
		t.Fatal("interrupted first write left an untracked application credential")
	}
}
