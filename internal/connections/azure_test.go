package connections

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
)

func TestAzureChecker(t *testing.T) {
	f := azuretest.New(t)
	c := AzureChecker{Client: azure.New(f.URL())}
	settings := azuretest.Settings()

	if _, err := c.Parse([]byte(azuretest.Secret), map[string]string{"tenant_id": azuretest.Tenant}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("parse without a client or subscription: %v", err)
	}
	// A Contributor lacks nothing. The resource group isn't there yet, so
	// it's checked in the subscription, and the resource providers aren't
	// registered, which a Contributor may do.
	got, err := c.Check(t.Context(), []byte(azuretest.Secret), settings)
	if err != nil || got.Account != "Acme production" || len(got.Missing) != 0 {
		t.Fatalf("contributor: %+v, %v", got, err)
	}
	want := map[string]string{"tenant_id": azuretest.Tenant, "client_id": azuretest.Client, "subscription_id": azuretest.Subscription, "resource_group": "loomplane"}
	if !maps.Equal(got.Details, want) {
		t.Fatalf("details %v", got.Details)
	}

	// A reader's check lists exactly what it may not do.
	f.Lock()
	f.Actions, f.NotActions = []string{"*/read"}, nil
	f.Unlock()
	reader, err := c.Check(t.Context(), []byte(azuretest.Secret), settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range reader.Missing {
		if strings.HasSuffix(m, "/read") {
			t.Errorf("a reader lacks %s", m)
		}
	}
	for _, need := range []string{"Microsoft.Resources/subscriptions/resourceGroups/write", "Microsoft.App/containerApps/write", "Microsoft.ContainerRegistry/registries/scheduleRun/action", "Microsoft.App/register/action"} {
		if !slices.Contains(reader.Missing, need) {
			t.Errorf("a reader's check doesn't list %s: %v", need, reader.Missing)
		}
	}

	if _, err := c.Check(t.Context(), []byte("wrong-secret"), settings); !rejected(err) || !strings.Contains(err.Error(), "AADSTS7000215") {
		t.Fatalf("a wrong secret: %v", err)
	}
	other := maps.Clone(settings)
	other["subscription_id"] = "44444444-4444-4444-4444-444444444444"
	if _, err := c.Check(t.Context(), []byte(azuretest.Secret), other); !rejected(err) || !strings.Contains(err.Error(), "no role in subscription") {
		t.Fatalf("a subscription it can't see: %v", err)
	}

	// The subscription and resource group are the scope: another
	// principal there is the same connection; another group isn't.
	id := AccountIdentity(Azure, "Acme production", got.Details)
	rotated := maps.Clone(got.Details)
	rotated["client_id"] = "55555555-5555-5555-5555-555555555555"
	if id == "" || id != AccountIdentity(Azure, "renamed", rotated) {
		t.Fatal("the identity changed with the principal or the subscription's name")
	}
	moved := maps.Clone(got.Details)
	moved["resource_group"] = "elsewhere"
	if id == AccountIdentity(Azure, "Acme production", moved) {
		t.Fatal("two resource groups share an identity")
	}
	if AccountIdentity(Azure, "x", map[string]string{"subscription_id": azuretest.Subscription}) != "" {
		t.Fatal("an unverified connection has an identity")
	}
}
