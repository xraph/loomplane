package recipes_test

import (
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestRegisterValidatesSettings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []recipes.Setting
		want     string
	}{
		{"bad name", []recipes.Setting{{Name: "Model", Title: "Model", Pattern: ".+"}}, `setting name "Model"`},
		{"empty pattern", []recipes.Setting{{Name: "model", Title: "Model"}}, `setting "model" has no pattern`},
		{"pattern that doesn't compile", []recipes.Setting{{Name: "model", Title: "Model", Pattern: "("}}, `setting "model" pattern "(" doesn't compile`},
		{"duplicate", []recipes.Setting{{Name: "model", Title: "Model", Pattern: ".+"}, {Name: "model", Title: "Again", Pattern: ".+"}}, `setting "model" is declared more than once`},
		{"default that doesn't match", []recipes.Setting{{Name: "size", Title: "Size", Pattern: "^[0-9]+$", Default: "x"}}, `setting "size" default "x"`},
		{"no title", []recipes.Setting{{Name: "model", Pattern: ".+"}}, `setting "model" has no title`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := recipestest.Sample()
			k.Settings = tc.settings
			err := recipes.NewRegistry().Register(k)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Register: %v, want an error with %q", err, tc.want)
			}
		})
	}

	k := recipestest.Sample()
	k.Settings = []recipes.Setting{
		{Name: "model", Title: "Model", Pattern: "[a-z0-9-]+/[a-z0-9.-]+", Required: true},
		{Name: "size", Title: "Size", Pattern: "^[0-9]+$", Default: "1"},
	}
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatalf("Register: %v", err)
	}
	k.Settings[0].Name = "changed"
	got, _ := r.Kind("sample")
	if len(got.Settings) != 2 || got.Settings[0].Name != "model" || got.Settings[1].Default != "1" {
		t.Fatalf("registered settings: %+v", got.Settings)
	}

	// A dependency-only kind has no manifest resource to set them.
	graph := recipestest.Graph()
	graph.Settings = []recipes.Setting{{Name: "size", Title: "Size", Pattern: "[0-9]+"}}
	if err := recipes.NewRegistry().Register(graph); err == nil || !strings.Contains(err.Error(), "takes no settings") {
		t.Fatalf("dependency-only kind with settings: %v", err)
	}
}

func TestSettingMatchesTheWholeValue(t *testing.T) {
	s := recipes.Setting{Name: "size", Title: "Size", Pattern: "[0-9]+"}
	// Twice, so the second round reads the compiled pattern.
	for range 2 {
		for value, want := range map[string]bool{"1": true, "12": true, "": false, "1x": false, "x1": false} {
			if got := s.Matches(value); got != want {
				t.Errorf("Matches(%q) = %v, want %v", value, got, want)
			}
		}
		if bad := (recipes.Setting{Name: "bad", Pattern: "("}); bad.Matches("(") || bad.Matches("") {
			t.Error("a pattern that doesn't compile matched")
		}
	}
}

func TestPerGPURequiresGPU(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(k *recipes.Kind)
	}{
		{"without a GPU", `process "sidecar" has per-GPU resources but requests no GPU`, func(k *recipes.Kind) {
			k.Components[1].Resources.PerGPU = &recipes.ResourceStep{CPUMillis: 100}
		}},
		{"negative", `process "server" per-GPU resources must not be negative`, func(k *recipes.Kind) {
			k.Components[0].Resources.PerGPU = &recipes.ResourceStep{MemoryMB: -1}
		}},
		{"shared memory overtakes memory", `process "server" shared memory must stay smaller than its memory, and at most 65536 MB, with 2 GPUs`, func(k *recipes.Kind) {
			// 8192+1024n memory against 2048+4096n shared memory: fine for 1, not for 2.
			k.Components[0].Resources.PerGPU = &recipes.ResourceStep{MemoryMB: 1024, ShmMB: 4096}
		}},
		{"shared memory past the cap", `with 8 GPUs`, func(k *recipes.Kind) {
			k.Components[0].Resources.PerGPU = &recipes.ResourceStep{MemoryMB: 16384, ShmMB: 8000}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := recipestest.GPUSample()
			tc.change(&k)
			err := recipes.NewRegistry().Register(k)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Register: %v, want an error with %q", err, tc.want)
			}
		})
	}

	k := recipestest.GPUSample()
	k.Components[0].Resources.PerGPU = &recipes.ResourceStep{CPUMillis: 1500, MemoryMB: 6144, ShmMB: 2048}
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatalf("Register: %v", err)
	}
	k.Components[0].Resources.PerGPU.CPUMillis = 1
	got, _ := r.Kind("gpusample")
	if step := got.Components[0].Resources.PerGPU; step == nil || step.CPUMillis != 1500 {
		t.Fatalf("registered step: %+v", step)
	}
}

func TestStartTimeoutBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		component int
		timeout   time.Duration
		want      string
	}{
		{"30 seconds", 1, 30 * time.Second, `process "api" start timeout 30s must be between a minute and three hours`},
		{"4 hours", 1, 4 * time.Hour, `process "api" start timeout 4h0m0s must be between a minute and three hours`},
		{"a job", 0, 10 * time.Minute, `job "migrate" has a start timeout; only processes do`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := recipestest.Sample()
			k.Components[tc.component].StartTimeout = tc.timeout
			err := recipes.NewRegistry().Register(k)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Register: %v, want an error with %q", err, tc.want)
			}
		})
	}
	for _, ok := range []time.Duration{time.Minute, 2 * time.Hour, 3 * time.Hour} {
		k := recipestest.Sample()
		k.Components[1].StartTimeout = ok
		if err := recipes.NewRegistry().Register(k); err != nil {
			t.Fatalf("start timeout %s: %v", ok, err)
		}
	}
}

func TestOptInAndSizeSettingRules(t *testing.T) {
	graph := recipestest.Graph()
	graph.EnvironmentOptIn = true
	if err := recipes.NewRegistry().Register(graph); err == nil || !strings.Contains(err.Error(), "can't opt in to environments") {
		t.Fatalf("dependency-only kind opting in: %v", err)
	}
	if err := recipes.NewRegistry().Register(recipestest.OptIn()); err != nil {
		t.Fatalf("opt-in kind: %v", err)
	}

	k := recipestest.GPUSample()
	k.Components[0].WeightCache.SizeSetting = "cache_mb"
	if err := recipes.NewRegistry().Register(k); err == nil || !strings.Contains(err.Error(), `weight cache size setting "cache_mb" isn't one of the kind's settings`) {
		t.Fatalf("size setting the kind doesn't take: %v", err)
	}
	k.Settings = []recipes.Setting{{Name: "cache_mb", Title: "Cache size", Pattern: "[0-9]{1,9}"}}
	if err := recipes.NewRegistry().Register(k); err != nil {
		t.Fatalf("size setting: %v", err)
	}
}

func TestWeightCacheSizeMB(t *testing.T) {
	w := recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480, SizeSetting: "cache_mb"}
	for value, want := range map[string]int{"153600": 153600, "1000": 20480, "": 20480, "-5": 20480, "+99999": 20480, "1e9": 20480} {
		if got := w.SizeMB(map[string]string{"cache_mb": value}); got != want {
			t.Errorf("cache_mb %q: %d, want %d", value, got, want)
		}
	}
	if got := (recipes.WeightCache{KubernetesSizeMB: 20480}).SizeMB(map[string]string{"cache_mb": "153600"}); got != 20480 {
		t.Errorf("no size setting: %d", got)
	}
}

// Ruling M17: the kinds list says what settings a kind takes, so a form
// can show them, and a kind without settings lists none.
func TestKindInfoListsSettings(t *testing.T) {
	var model, postgres *recipes.KindInfo
	for _, info := range recipes.Default().Kinds() {
		switch info.Name {
		case recipes.ModelLlamaCPPKind:
			model = &info
		case "postgres":
			postgres = &info
		}
	}
	if model == nil || postgres == nil {
		t.Fatal("the kinds list has no model-llamacpp or postgres")
	}
	k, _ := recipes.Default().Kind(recipes.ModelLlamaCPPKind)
	if len(model.Settings) != len(k.Settings) || len(k.Settings) == 0 {
		t.Fatalf("model-llamacpp lists %d settings, the kind has %d", len(model.Settings), len(k.Settings))
	}
	for i, s := range k.Settings {
		want := recipes.SettingInfo(s)
		if model.Settings[i] != want {
			t.Errorf("setting %d: %+v, want %+v", i, model.Settings[i], want)
		}
	}
	if len(postgres.Settings) != 0 {
		t.Errorf("postgres lists settings %+v", postgres.Settings)
	}
}
