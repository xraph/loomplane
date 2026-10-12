package planning

import (
	"encoding/json"
	"strings"
	"testing"
)

// deployedAs is what deploying res at commit would have recorded.
func deployedAs(t *testing.T, res *Result, commit string) []*Binding {
	t.Helper()
	out := make([]*Binding, 0, len(res.Actions))
	for _, a := range res.Actions {
		cfg, err := json.Marshal(a.Service)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &Binding{
			ID: "bind_" + a.Name, Subject: a.Subject, Name: a.Name, Provider: a.Provider, Region: a.Region,
			Engine: a.Engine, Config: cfg, CommitSHA: commit, Revision: "rev-" + a.Name,
		})
	}
	return out
}

func planWith(t *testing.T, manifest, commit string, bindings []*Binding) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: polyglot(t), Manifest: parse(t, manifest), Providers: deployable(t),
		Connections: connected, Bindings: bindings, CommitSHA: commit,
	})
}

func TestCompute_newCodeUpdatesServices(t *testing.T) {
	first := planWith(t, webAndAPI, "aaaaaaaaaaaa", nil)
	deployed := deployedAs(t, first, "aaaaaaaaaaaa")

	same := planWith(t, webAndAPI, "aaaaaaaaaaaa", deployed)
	for _, a := range same.Actions {
		if a.Op != Keep {
			t.Fatalf("same commit: %s %s", a.Op, a.Name)
		}
	}

	next := planWith(t, webAndAPI, "bbbbbbbbbbbb", deployed)
	for _, a := range next.Actions {
		if a.Op != Update || len(a.Changes) != 1 || a.Changes[0].Field != "commit" ||
			a.Changes[0].From != "aaaaaaa" || a.Changes[0].To != "bbbbbbb" || !strings.Contains(a.Reason, "new code") {
			t.Fatalf("new commit: %+v", a)
		}
	}
}

func TestCompute_driftIsPlannedBack(t *testing.T) {
	first := planWith(t, webAndAPI, "aaaaaaaaaaaa", nil)
	deployed := deployedAs(t, first, "aaaaaaaaaaaa")
	for _, b := range deployed {
		switch b.Name {
		case "api":
			b.Drift = DriftMissing
		case "web":
			b.Drift, b.DriftMessage = DriftChanged, "another deployment is live"
		}
	}
	res := planWith(t, webAndAPI, "aaaaaaaaaaaa", deployed)
	api, web := action(t, res, "service:api"), action(t, res, "service:web")
	if api.Op != Create || !strings.Contains(api.Reason, "deleted outside Loomplane") {
		t.Fatalf("api %+v", api)
	}
	if web.Op != Update || !strings.Contains(web.Reason, "another deployment is live") {
		t.Fatalf("web %+v", web)
	}
}

func TestCompute_aMissingServiceTheManifestDropsIsDeleted(t *testing.T) {
	first := planWith(t, webAndAPI, "aaaaaaaaaaaa", nil)
	deployed := deployedAs(t, first, "aaaaaaaaaaaa")
	for _, b := range deployed {
		if b.Name == "web" {
			b.Drift = DriftMissing
		}
	}
	apiOnly := "version: 1\nservices:\n  api:\n    source: services/api\n    provider: cloud-run\n"
	res := planWith(t, apiOnly, "aaaaaaaaaaaa", deployed)
	if web := action(t, res, "service:web"); web.Op != Delete {
		t.Fatalf("web %+v", web)
	}
}
