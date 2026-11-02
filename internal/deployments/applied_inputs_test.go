package deployments

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
)

type snapshotSecrets map[string][]byte

func (s snapshotSecrets) Put(_ context.Context, k string, v []byte) error {
	s[k] = append([]byte(nil), v...)
	return nil
}
func (s snapshotSecrets) Get(_ context.Context, k string) ([]byte, error) {
	v, ok := s[k]
	if !ok {
		return nil, ErrNoCredential
	}
	return append([]byte(nil), v...), nil
}
func (s snapshotSecrets) Delete(_ context.Context, k string) error { delete(s, k); return nil }

func TestAppliedVariablesSurviveRetryAndRefreshGeneratedLinks(t *testing.T) {
	ctx := t.Context()
	secrets := snapshotSecrets{}
	vars := fakeVariables{"svc": {"SECRET_TOKEN": "original", "OVERRIDE": "manual"}}
	r := NewRunner(RunnerDeps{Variables: vars, Resources: secrets})
	d := &Deployment{ID: "dep", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	a := planning.Action{ID: "service:web", Name: "web", Service: &planning.ServiceConfig{Service: "svc", Env: []planning.EnvLink{{Key: "API", From: "api"}, {Key: "OVERRIDE", From: "api"}}}}
	if err := r.captureAppliedVariables(ctx, d, &a, nil, nil); err != nil {
		t.Fatal(err)
	}
	ref := a.Service.AppliedInputs.Variables
	vars["svc"]["SECRET_TOKEN"] = "new-secret"
	retry := planning.Action{ID: a.ID, Service: &planning.ServiceConfig{Service: "svc", Env: a.Service.Env}}
	if err := r.captureAppliedVariables(ctx, d, &retry, nil, nil); err != nil {
		t.Fatal(err)
	}
	if retry.Service.AppliedInputs.Variables != ref {
		t.Fatal("retry changed snapshot")
	}
	env, mask, err := r.env(ctx, d, retry, map[string]*planning.Binding{"api": {URL: "https://new-api"}}, nil)
	if err != nil || env["SECRET_TOKEN"] != "original" || env["API"] != "https://new-api" || env["OVERRIDE"] != "manual" || !mask["SECRET_TOKEN"] {
		t.Fatalf("snapshot or generated links changed: %v", err)
	}
	raw, _ := json.Marshal(retry)
	if string(raw) == "" || containsSecret(raw, "original") {
		t.Fatal("secret exposed in action")
	}
}
func containsSecret(raw []byte, value string) bool {
	for i := 0; i+len(value) <= len(raw); i++ {
		if string(raw[i:i+len(value)]) == value {
			return true
		}
	}
	return false
}

func TestAppliedSourceUsesEachBindingsArtifact(t *testing.T) {
	r := NewRunner(RunnerDeps{Sources: &fakeSources{files: map[string]string{"old-api": "api-old-content", "new-web": "web-new-content"}}})
	d := &Deployment{Sources: []SourceArtifact{{Alias: "app", Key: "new-web", CommitSHA: "new"}}}
	a := planning.Action{Service: &planning.ServiceConfig{Repo: "app", CommitSHA: "old", AppliedInputs: &planning.AppliedInputs{Source: &planning.PlanSource{Alias: "app", CommitSHA: "old"}, ArtifactKey: "old-api"}}}
	src, _, err := r.appliedSource(t.Context(), d, a)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	body, err := io.ReadAll(src)
	if err != nil || string(body) != "api-old-content" {
		t.Fatalf("built wrong content %q: %v", body, err)
	}
}
