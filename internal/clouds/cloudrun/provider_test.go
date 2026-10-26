package cloudrun

import (
	"encoding/json"
	"strings"
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"
)

func TestOrdinaryDeploysSendNoTraffic(t *testing.T) {
	spec := cpprovider.ServiceSpec{Name: "main", Image: "europe-west1-docker.pkg.dev/p/loomplane/api:abc", Env: map[string]string{"MODE": "fast"}}
	labels := map[string]string{LabelPublic: "true", LabelSecret + "TOKEN": "lp-api-TOKEN:4"}
	svc, err := Service(spec, labels)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Traffic != nil || svc.Template.Revision != "" {
		t.Fatalf("traffic %+v, revision %q", svc.Traffic, svc.Template.Revision)
	}
	data, err := json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}
	// Without traffic, Cloud Run sends all of it to the latest revision.
	if strings.Contains(string(data), `"traffic"`) || strings.Contains(string(data), `"revision"`) {
		t.Fatalf("an ordinary deploy sends %s", data)
	}
	env := svc.Template.Containers[0].Env
	if len(env) != 2 || env[0].Value != "fast" || env[1].ValueSource == nil || env[1].ValueSource.SecretKeyRef.Version != "4" {
		t.Fatalf("env %+v", env)
	}
}

func TestServiceRefusesASecretWithoutAVersion(t *testing.T) {
	if _, err := Service(cpprovider.ServiceSpec{Image: "x"}, map[string]string{LabelSecret + "TOKEN": "lp-api-TOKEN"}); err == nil {
		t.Fatal("a secret without a version was built")
	}
}
