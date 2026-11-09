package connections

import (
	"context"
	"errors"
	"maps"
	"testing"
)

type preparingChecker struct {
	identityChecker
	calls    int
	prepared map[string]string
}

func (c *preparingChecker) Prepare(_ context.Context, _ []byte, settings map[string]string) (map[string]string, error) {
	c.calls++
	out := maps.Clone(settings)
	if out == nil {
		out = map[string]string{}
	}
	out["architecture"] = "arm64"
	c.prepared = maps.Clone(out)
	return out, nil
}
func (c *preparingChecker) Check(_ context.Context, _ []byte, settings map[string]string) (Check, error) {
	if settings["storage_class"] == "delete-default" && settings["architecture"] == "" {
		return Check{}, errors.New("storage still needs preparation")
	}
	result := c.result
	result.Details = maps.Clone(c.result.Details)
	if settings["architecture"] != "" {
		result.Details["architecture"] = settings["architecture"]
	}
	return result, nil
}

func (c *preparingChecker) PreparationIdentity(context.Context, []byte, map[string]string) (Check, error) {
	return c.result, nil
}

func TestConnectionPrepareOnlyOnSaveAndAfterReplacementIdentity(t *testing.T) {
	checker := &preparingChecker{identityChecker: identityChecker{result: Check{Account: "cluster/apps", Details: map[string]string{"endpoint": "https://cluster", "namespace": "apps", "namespace_uid": "apps-uid", "cluster_ca": "ca"}}}}
	store := &memStore{rows: map[string]Connection{}}
	vault := &memSecrets{kept: map[string][]byte{}}
	service := NewService(store, vault, map[string]Checker{Kubernetes: checker}, identityAuthz{}, nil, nil)
	connection, err := service.Create(t.Context(), orgPrincipal, CreateInput{Cloud: Kubernetes, Name: "Local cluster", Credential: "key", Settings: map[string]string{"registry_hostname": "images.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 || connection.Details["architecture"] != "arm64" {
		t.Fatal("save did not retain prepared settings")
	}
	if _, err = service.Recheck(t.Context(), orgPrincipal, connection.ID); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 {
		t.Fatal("recheck ran setup")
	}
	checker.result.Details["namespace_uid"] = "replacement-namespace"
	if _, err = service.Replace(t.Context(), orgPrincipal, connection.ID, ReplaceInput{Credential: "other"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed identity accepted: %v", err)
	}
	if checker.calls != 1 {
		t.Fatal("setup mutated a replacement namespace before identity validation")
	}
	checker.result.Details["namespace_uid"] = "apps-uid"
	if _, err = service.Replace(t.Context(), orgPrincipal, connection.ID, ReplaceInput{Credential: "rotated", Settings: map[string]string{"storage_class": "delete-default"}}); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 2 {
		t.Fatal("save did not resume setup")
	}
	settings := map[string]string{"ingress_address": "192.0.2.40", "registry_ingress_address": "automatic"}
	if _, err = service.Replace(t.Context(), orgPrincipal, connection.ID, ReplaceInput{Credential: "rotated", Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if checker.prepared["ingress_address"] != "192.0.2.40" || checker.prepared["registry_ingress_address"] != "" {
		t.Fatal("manual ingress override kept automatic discovery enabled")
	}
	if settings["registry_ingress_address"] != "automatic" {
		t.Fatal("replacement mutated caller settings")
	}
}
