package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestFederationImagesUseReviewedRegistry(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("b", 64) + `","size":100,"platform":{"os":"linux","architecture":"arm64"}}]}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	calls, credentialsRead := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v2/team/api/manifests/"+digest || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("wrong manifest request", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		w.Write(raw)
	}))
	defer server.Close()
	store, b, inst, d, base := federationControllerFixture()
	image := strings.TrimPrefix(server.URL, "http://") + "/team/api@" + digest
	b.Revision, inst.Services[0].Image, store.receipt.Image = image, image, image
	connection := &connections.Connection{ID: "registry", OrgID: b.OrgID, Cloud: connections.Registry, Status: connections.StatusReady, Details: map[string]string{"endpoint": server.URL, "repository": "team"}}
	reviewed := planning.ReviewedConnectionSettings(connection.Cloud, connection.Details)
	config := planning.ServiceConfig{BuildTarget: &planning.BuildTarget{Registry: connection.ID, RegistrySettings: reviewed}}
	b.Config, _ = json.Marshal(config)
	inst.Labels[labelRegistry] = connection.ID
	settings, _ := json.Marshal(reviewed)
	inst.Labels[labelRegistrySettings] = string(settings)
	secret := []byte(`{"token":"fixture-token"}`)
	creds := buildCredentialFunc(func(_ context.Context, org, key string) (*connections.Connection, []byte, error) {
		credentialsRead++
		secret = []byte(`{"token":"fixture-token"}`)
		if org != b.OrgID || key != connection.ID {
			t.Error("credential scope mismatch", org, key)
		}
		return connection, secret, nil
	})
	reported := "untrusted.example/import@sha256:" + strings.Repeat("b", 64)
	profile, err := LoadFederationController(t.Context(), store, b, inst, d, base)
	if err != nil {
		t.Fatal(err)
	}
	profile.ImageIndexes, err = LoadFederationImageProofs(t.Context(), creds, b, inst, reported, "linux", "arm64")
	if err != nil || len(profile.ImageIndexes) != 1 || string(profile.ImageIndexes[0]) != string(raw) || calls != 1 || credentialsRead != 1 {
		t.Fatal("registry proof not loaded", err, calls, credentialsRead)
	}
	for _, v := range secret {
		if v != 0 {
			t.Fatal("credential bytes retained")
		}
	}
	for _, scenario := range []string{"foreign registry", "changed trust", "foreign instance", "uncommitted plan", "missing registry"} {
		t.Run(scenario, func(t *testing.T) {
			oldID, oldSettings, oldPlan, oldTenant := inst.Labels[labelRegistry], inst.Labels[labelRegistrySettings], inst.Labels["loomplane.plan"], inst.TenantID
			defer func() {
				inst.Labels[labelRegistry], inst.Labels[labelRegistrySettings], inst.Labels["loomplane.plan"], inst.TenantID = oldID, oldSettings, oldPlan, oldTenant
			}()
			switch scenario {
			case "foreign registry":
				inst.Labels[labelRegistry] = "other"
			case "changed trust":
				inst.Labels[labelRegistrySettings] = `{"endpoint":"https://foreign.example"}`
			case "foreign instance":
				inst.TenantID = "other"
			case "uncommitted plan":
				inst.Labels["loomplane.plan"] = "other"
			case "missing registry":
				inst.Labels[labelRegistry] = ""
			}
			if _, err := LoadFederationImageProofs(t.Context(), creds, b, inst, reported, "linux", "arm64"); err == nil {
				t.Fatal("unreviewed registry used")
			}
			if calls != 1 || credentialsRead != 1 {
				t.Fatal("invalid deployment reached credential or network", calls, credentialsRead)
			}
		})
	}
	// A connection can change after review even when instance labels still match.
	connection.Details["endpoint"] = "https://changed.example"
	if _, err := LoadFederationImageProofs(t.Context(), creds, b, inst, reported, "linux", "arm64"); err == nil {
		t.Fatal("changed current registry trust accepted")
	}
	if calls != 1 || credentialsRead != 2 {
		t.Fatal("changed registry reached network", calls, credentialsRead)
	}
	for _, v := range secret {
		if v != 0 {
			t.Fatal("rejected credential bytes retained")
		}
	}
}
func TestFederationImagesSameDigestNeedsNoRegistry(t *testing.T) {
	_, b, inst, _, _ := federationControllerFixture()
	_, digest, _ := strings.Cut(b.Revision, "@")
	proofs, err := LoadFederationImageProofs(t.Context(), nil, b, inst, "alias.example/api@"+digest, "linux", "arm64")
	if err != nil || len(proofs) != 0 {
		t.Fatal("identical content needs registry", err)
	}
}
