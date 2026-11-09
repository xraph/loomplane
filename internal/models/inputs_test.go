package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

// modelManifest is a manifest with one model resource, llama-8b, of kind
// with settings, enabled in staging on one server.
func modelManifest(t *testing.T, kind string, settings map[string]string) *manifest.Manifest {
	t.Helper()
	r := map[string]any{"kind": kind, "provider": "server", "target": serverID, "settings": settings,
		"environments": map[string]any{"staging": map[string]any{"enabled": true}}}
	if gpuKind(kind) {
		r["gpu"] = map[string]any{"kind": "nvidia-l40s", "count": 1}
	}
	doc, err := json.Marshal(map[string]any{"version": 1, "resources": map[string]any{"llama-8b": r}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// digestOf digests llama-8b as m has it.
func digestOf(t *testing.T, in Inputs, m *manifest.Manifest) map[string]string {
	t.Helper()
	d, err := in.Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", m.Resources["llama-8b"])
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDigestChangesWithArgsAndToken(t *testing.T) {
	reg := fixture(t, llamaRevision, "--enforce-eager")
	tokens := &fakeTokens{token: hfToken, revision: 1}
	settings := settingsFor(t, reg, llamaID, "bf16")
	delete(settings, "max_model_len") // the kind's default fills it
	m := modelManifest(t, recipes.ModelVLLMKind, settings)
	in := Inputs{Registry: reg, Settings: tokens}

	base := digestOf(t, in, m)
	if len(base) != 2 || !strings.HasPrefix(base[recipes.ModelEngine], "sha256:") || base[recipes.ModelGuard] != digest(engines.GuardConfig()) {
		t.Fatalf("digest = %v", base)
	}
	if again := digestOf(t, in, m); !maps.Equal(again, base) {
		t.Errorf("the same inputs digest differently: %v, %v", base, again)
	}
	// The engine's digest is its arguments, as the engine gets them, and
	// the token's revision; the guard's is its Caddyfile.
	filled, ok := kindSettings(mustKind(t, recipes.ModelVLLMKind), settings)
	if !ok || filled["max_model_len"] != "4096" {
		t.Fatalf("settings with defaults: %v, %v", filled, ok)
	}
	spec, err := engines.FromSettings(context.Background(), reg, nil, "org", filled, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	args, err := engines.Args(spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := digest([]byte(strings.Join(args, "\n") + "\nhf-token-revision:1")); base[recipes.ModelEngine] != want {
		t.Errorf("engine digest %s, want %s", base[recipes.ModelEngine], want)
	}

	// A registry change to the entry's arguments changes the engine's digest
	// and not the guard's.
	in.Registry = fixture(t, llamaRevision, "--max-num-seqs=64")
	changed := digestOf(t, in, m)
	if changed[recipes.ModelEngine] == base[recipes.ModelEngine] || changed[recipes.ModelGuard] != base[recipes.ModelGuard] {
		t.Errorf("args changed: %v, was %v", changed, base)
	}
	in.Registry = reg

	// A new token for a gated model changes it too.
	tokens.revision = 2
	if rotated := digestOf(t, in, m); rotated[recipes.ModelEngine] == base[recipes.ModelEngine] {
		t.Error("a new token left the gated engine's digest alone")
	}

	// A mirror changes it, and huggingface.co named outright doesn't.
	hub := digestOf(t, in, m)
	in.HuggingFaceURL = "https://huggingface.co/"
	if same := digestOf(t, in, m); !maps.Equal(same, hub) {
		t.Errorf("huggingface.co named outright changed the digest: %v, was %v", same, hub)
	}
	in.HuggingFaceURL = "https://hf-mirror.example.com"
	if mirrored := digestOf(t, in, m); mirrored[recipes.ModelEngine] == hub[recipes.ModelEngine] || mirrored[recipes.ModelGuard] != hub[recipes.ModelGuard] {
		t.Errorf("a mirror: %v, was %v", mirrored, hub)
	}
	in.HuggingFaceURL = ""

	// An ungated model never downloads with the token, so its digest
	// doesn't follow it.
	qwen := settingsFor(t, registry.Default(), "Qwen/Qwen2.5-7B-Instruct", "bf16")
	ungated, qm := Inputs{Registry: registry.Default(), Settings: tokens}, modelManifest(t, recipes.ModelVLLMKind, qwen)
	first := digestOf(t, ungated, qm)
	tokens.revision = 3
	if second := digestOf(t, ungated, qm); !maps.Equal(first, second) {
		t.Errorf("a new token changed an ungated model's digest: %v, %v", first, second)
	}

	// The digest never holds the token.
	for _, d := range []map[string]string{base, changed, first} {
		for _, v := range d {
			if strings.Contains(v, hfToken) {
				t.Errorf("digest %q holds the token", v)
			}
		}
	}
}

func mustKind(t *testing.T, name string) recipes.Kind {
	t.Helper()
	k, ok := recipes.Default().Kind(name)
	if !ok {
		t.Fatalf("no kind %s", name)
	}
	return k
}

// Settings the kind itself refuses get no digest, so the planner reports
// each at its path instead of the plan failing on the engine's account of
// it.
func TestDigestLeavesBadSettingsToThePlanner(t *testing.T) {
	reg := fixture(t, llamaRevision, "--enforce-eager")
	in := Inputs{Registry: reg, Settings: &fakeTokens{token: hfToken, revision: 1}}
	good := settingsFor(t, reg, llamaID, "gguf-q4_k_m")
	for name, edit := range map[string]func(map[string]string){
		"malformed revision": func(s map[string]string) { s["revision"] = "main" },
		"no served name":     func(s map[string]string) { delete(s, "served_name") },
		"no GGUF file":       func(s map[string]string) { delete(s, "file") },
		"unknown setting":    func(s map[string]string) { s["dtype"] = "half" },
		"bad context":        func(s map[string]string) { s["max_model_len"] = "4k" },
	} {
		settings := maps.Clone(good)
		edit(settings)
		m := modelManifest(t, recipes.ModelLlamaCPPKind, settings)
		d, err := in.Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", m.Resources["llama-8b"])
		if err != nil || d != nil {
			t.Errorf("%s: %v, %v, want no digest and no error", name, d, err)
			continue
		}
		// The planner reports it at the setting's path.
		r := planWith(t, m, d, nil)
		if r.Executable || !slices.ContainsFunc(r.Problems, func(f planning.Finding) bool {
			return strings.HasPrefix(f.Path, "resources.llama-8b.settings.") && strings.HasPrefix(f.Code, "catalog-setting-")
		}) {
			t.Errorf("%s: plan problems %+v", name, r.Problems)
		}
	}
}

// A gated model with no workspace token fails the plan with the sentence
// that says what to do, instead of planning an engine that can't start.
func TestDigestOfAGatedModelWithoutAToken(t *testing.T) {
	reg := fixture(t, llamaRevision, "--enforce-eager")
	m := modelManifest(t, recipes.ModelVLLMKind, settingsFor(t, reg, llamaID, "bf16"))
	tokens := &fakeTokens{token: hfToken, revision: 1}
	in := Inputs{Registry: reg, Settings: tokens}
	if _, err := in.Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", m.Resources["llama-8b"]); err != nil {
		t.Fatalf("with a token: %v", err)
	}
	const sentence = "Llama 3.1 8B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings"
	for name, in := range map[string]Inputs{"token cleared": {Registry: reg, Settings: &fakeTokens{}}, "no settings": {Registry: reg}} {
		_, err := in.Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", m.Resources["llama-8b"])
		var gated *GatedError
		if !errors.As(err, &gated) || err.Error() != sentence {
			t.Errorf("%s: %v, want %q", name, err, sentence)
		}
	}
	// An ungated model doesn't need one.
	qwen := modelManifest(t, recipes.ModelVLLMKind, settingsFor(t, registry.Default(), "Qwen/Qwen2.5-7B-Instruct", "bf16"))
	if _, err := (Inputs{Registry: registry.Default(), Settings: &fakeTokens{}}).Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", qwen.Resources["llama-8b"]); err != nil {
		t.Errorf("ungated without a token: %v", err)
	}
}

// The digest's errors become the plan's failure reason, so none may hold
// the token, and a re-pinned resource says what to set (Ruling M21).
func TestDigestErrorsCarryNoSecret(t *testing.T) {
	reg := fixture(t, llamaRepin, "--enforce-eager")
	tokens := &fakeTokens{token: hfToken, revision: 1}
	old := settingsFor(t, fixture(t, llamaRevision, "--enforce-eager"), llamaID, "bf16")
	unknown := maps.Clone(settingsFor(t, reg, llamaID, "bf16"))
	unknown["quantization"] = "fp4"
	// The GGUF's file isn't a vLLM setting; without it the engine is the
	// only thing wrong.
	gguf := settingsFor(t, reg, llamaID, "gguf-q4_k_m")
	delete(gguf, "file")

	for name, c := range map[string]struct {
		kind     string
		settings map[string]string
		want     string
	}{
		"re-pinned":     {recipes.ModelVLLMKind, old, "but the model registry now pins meta-llama/Llama-3.1-8B-Instruct bf16 to meta-llama/Llama-3.1-8B-Instruct@" + llamaRepin + ". Set llama-8b's settings to model: meta-llama/Llama-3.1-8B-Instruct, revision: " + llamaRepin},
		"no such quant": {recipes.ModelVLLMKind, unknown, `has no quantization "fp4"`},
		"wrong engine":  {recipes.ModelVLLMKind, gguf, "Deploy it as model-llamacpp-gpu"},
	} {
		in := Inputs{Registry: reg, Settings: tokens}
		_, err := in.Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", modelManifest(t, c.kind, c.settings).Resources["llama-8b"])
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), hfToken) || secretLike.MatchString(err.Error()) {
			t.Errorf("%s: the error holds the token: %v", name, err)
		}
	}
}

// secretLike matches a Hugging Face token or a model API key: "hf_" or
// "lpm_" and then what a secret's characters look like. The plan holds the
// key's prefix and the token file's path, and neither is followed by one.
var secretLike = regexp.MustCompile(`(hf|lpm)_[A-Za-z0-9_-]{8,}`)

const serverID = "conn_01m4vewksnea2bwdxwdy5aekhn"

// planWith plans m in staging with the model's digests, over bound.
func planWith(t *testing.T, m *manifest.Manifest, digests map[string]string, bound []*planning.Binding) *planning.Result {
	t.Helper()
	features := map[string]bool{recipes.FeatureAI: true}
	return planning.Compute(planning.Input{
		Environment: "staging", Graph: &graph.Graph{}, Manifest: m, Providers: providers.RegistryFor(features), Features: features,
		Connections: []planning.ConnectionOption{{ID: serverID, Name: "one", Cloud: "server", Ready: true, Details: map[string]string{
			"host": "one.example.com", "user": "deploy", "docker_socket": "/var/run/docker.sock", "architecture": "amd64",
		}}},
		Inputs: map[string]map[string]string{"llama-8b": digests}, Bindings: bound,
	})
}

// boundFrom is what deploying r's resource actions would have bound.
func boundFrom(t *testing.T, r *planning.Result) []*planning.Binding {
	t.Helper()
	var out []*planning.Binding
	for _, a := range r.Actions {
		if a.Resource == nil {
			continue
		}
		raw, err := json.Marshal(a.Resource)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &planning.Binding{ID: "binding-" + a.Name, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: a.ConnectionID, ExternalID: "instance-" + a.Name, Engine: a.Engine, Config: raw})
	}
	return out
}

// A new token for a gated model plans an update of its engine alone, and
// nothing the plan records holds the token or the model's API key.
func TestNewTokenPlansAnUpdateWithoutTheToken(t *testing.T) {
	reg := fixture(t, llamaRevision, "--enforce-eager")
	tokens := &fakeTokens{token: hfToken, revision: 1}
	m := modelManifest(t, recipes.ModelLlamaCPPKind, settingsFor(t, reg, llamaID, "gguf-q4_k_m"))
	in := Inputs{Registry: reg, Settings: tokens}

	first := planWith(t, m, digestOf(t, in, m), nil)
	if !first.Executable {
		t.Fatalf("first plan: %+v", first.Problems)
	}
	tokens.revision = 2
	second := planWith(t, m, digestOf(t, in, m), boundFrom(t, first))
	if !second.Executable {
		t.Fatalf("second plan: %+v", second.Problems)
	}
	for _, a := range second.Actions {
		switch {
		case a.ID == "resource:llama-8b--engine" && (a.Op != planning.Update || a.Reason != "llama-8b--engine's configuration changed."):
			t.Errorf("engine: %s %q", a.Op, a.Reason)
		case a.ID != "resource:llama-8b--engine" && a.Op != planning.Keep:
			t.Errorf("%s: %s %q, want keep", a.ID, a.Op, a.Reason)
		}
	}
	for _, r := range []*planning.Result{first, second} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), hfToken) || secretLike.Match(raw) {
			t.Errorf("the plan holds a secret:\n%s", raw)
		}
	}
}

// A re-pin and a missing token are problems a plan shows as they are, with
// the fix in them, at planning and at approval (planning.InputsProblem).
func TestDigestProblemsKeepTheirFix(t *testing.T) {
	reg := fixture(t, llamaRepin, "--enforce-eager")
	repinned := modelManifest(t, recipes.ModelVLLMKind, settingsFor(t, fixture(t, llamaRevision, "--enforce-eager"), llamaID, "bf16"))
	gated := modelManifest(t, recipes.ModelVLLMKind, settingsFor(t, reg, llamaID, "bf16"))
	for name, c := range map[string]struct {
		in   Inputs
		m    *manifest.Manifest
		want string
	}{
		"re-pinned": {Inputs{Registry: reg, Settings: &fakeTokens{token: hfToken, revision: 1}}, repinned, "then plan again"},
		"no token":  {Inputs{Registry: reg, Settings: &fakeTokens{}}, gated, "in AI settings"},
	} {
		_, err := c.in.Digest(context.Background(), "org", "shop", "env_staging", "llama-8b", c.m.Resources["llama-8b"])
		var problem planning.InputsProblem
		if !errors.As(fmt.Errorf("wrapped: %w", err), &problem) || !strings.HasSuffix(problem.Error(), c.want) {
			t.Errorf("%s: %v is no InputsProblem ending %q", name, err, c.want)
		}
	}
}
