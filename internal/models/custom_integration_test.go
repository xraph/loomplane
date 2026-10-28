//go:build integration

package models

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
)

// builtQwen is the entry Build makes from the Qwen fixture.
func builtQwen(t *testing.T) registry.Entry {
	t.Helper()
	config, err := os.ReadFile("hfhub/testdata/qwen2.5-7b-instruct-config.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := hfhub.Build(&hfhub.ModelInfo{ID: "Qwen/Qwen2.5-7B-Instruct", SHA: "a09a35458c702b33eeacc393d103063234e8bc28",
		PipelineTag: "text-generation", License: "apache-2.0",
		Files: []hfhub.FileInfo{{Name: "model.safetensors", Size: 15231271888}}}, config)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCustomModelsAreOrgScoped(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	e := builtQwen(t)

	if err := st.PutCustom(ctx, "org_a", e); err != nil {
		t.Fatal(err)
	}
	got, err := st.Custom(ctx, "org_a", e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, e) {
		t.Fatalf("read back:\n got %+v\nwant %+v", got, e)
	}
	// Another workspace never sees it, by ID or in its list.
	if _, err := st.Custom(ctx, "org_b", e.ID); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("org_b reads org_a's entry: %v", err)
	}
	if list, err := st.ListCustom(ctx, "org_b"); err != nil || len(list) != 0 {
		t.Errorf("org_b lists %d entries, %v", len(list), err)
	}
	if list, err := st.ListCustom(ctx, "org_a"); err != nil || len(list) != 1 || list[0].ID != e.ID {
		t.Errorf("org_a lists %v, %v", list, err)
	}

	// The ID pins the commit, so storing it again keeps the first copy.
	changed := e
	changed.Title = "something else"
	if err := st.PutCustom(ctx, "org_a", changed); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Custom(ctx, "org_a", e.ID); got.Title != e.Title {
		t.Errorf("a second put replaced the entry: title %q", got.Title)
	}
	// org_b may store the same repo for itself.
	if err := st.PutCustom(ctx, "org_b", e); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Custom(ctx, "org_b", e.ID); err != nil {
		t.Errorf("org_b's own copy: %v", err)
	}

	// An entry Build wouldn't make is refused, and so is one edited into
	// the table by hand.
	bad := e
	bad.TrustRemoteCode = true
	if err := st.PutCustom(ctx, "org_a", bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("trust_remote_code: %v", err)
	}
	raw, _ := json.Marshal(bad)
	if _, err := st.db.Exec(ctx, `UPDATE loomplane_ai_custom_models SET entry = $1 WHERE org_id = 'org_a'`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Custom(ctx, "org_a", e.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("a stored entry asking for remote code: %v", err)
	}
	if _, err := st.ListCustom(ctx, "org_a"); !errors.Is(err, ErrInvalid) {
		t.Errorf("listing it: %v", err)
	}
}
