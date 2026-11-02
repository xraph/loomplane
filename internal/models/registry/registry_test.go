package registry

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func loadFixture(t *testing.T, dir string) (*Registry, error) {
	t.Helper()
	return Load(os.DirFS("testdata/" + dir))
}

func mustLoadGood(t *testing.T) *Registry {
	t.Helper()
	r, err := loadFixture(t, "good")
	if err != nil {
		t.Fatalf("Load(testdata/good): %v", err)
	}
	return r
}

func TestLoadGoodFixture(t *testing.T) {
	r := mustLoadGood(t)

	entries := r.Entries()
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Family+" "+e.ID)
	}
	want := []string{
		"acme acme/Small-1B-Instruct",
		"acme acme/Embed-Small",
		"beta beta/Reasoner-16B",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Entries() order:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	e, ok := r.Entry("acme/Small-1B-Instruct")
	if !ok {
		t.Fatal(`Entry("acme/Small-1B-Instruct") not found`)
	}
	if e.Family != "acme" {
		t.Errorf("Family = %q, want acme", e.Family)
	}
	if e.Tasks[0] != TaskChat {
		t.Errorf("primary task = %q, want chat", e.Tasks[0])
	}
	if q, ok := e.Quantization("gguf-q4_k_m"); !ok || q.Engine != EngineLlamaCPP || q.File != "Small-1B-Instruct-Q4_K_M.gguf" {
		t.Errorf(`Quantization("gguf-q4_k_m") = %+v, %v`, q, ok)
	}
	if _, ok := e.Quantization("fp8"); ok {
		t.Error(`Quantization("fp8") found on an entry without it`)
	}
	if _, ok := r.Entry("acme/small-1b-instruct"); ok {
		t.Error("Entry is case-insensitive, want case-sensitive")
	}

	embed, _ := r.Entry("acme/Embed-Small")
	if embed.Embedding == nil || embed.Embedding.Dimensions != 384 || embed.Embedding.QueryPrefix != "query: " {
		t.Errorf("embedding block = %+v", embed.Embedding)
	}
}

func TestEntriesAreCopies(t *testing.T) {
	r := mustLoadGood(t)
	entries := r.Entries()
	entries[0].Tasks[0] = TaskVision
	entries[0].Quantizations[0].ID = "fp8"

	e, _ := r.Entry("acme/Small-1B-Instruct")
	if e.Tasks[0] != TaskChat || e.Quantizations[0].ID != "bf16" {
		t.Fatalf("changing a returned entry changed the registry: %+v", e)
	}
}

func TestLoadIsStrict(t *testing.T) {
	_, err := loadFixture(t, "bad-unknown-field")
	if err == nil {
		t.Fatal("Load accepted an unknown field")
	}
	for _, want := range []string{"acme.yaml", "quantisations"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't name %q", err, want)
		}
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error %v isn't ErrInvalid", err)
	}
}

func TestFamilyMustMatchFileName(t *testing.T) {
	_, err := loadFixture(t, "bad-family")
	if err == nil {
		t.Fatal("Load accepted a family that doesn't match its file")
	}
	if want := `family "acme" in other.yaml`; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q doesn't contain %q", err, want)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error %v isn't ErrInvalid", err)
	}
}

func TestDuplicateIDsAcrossFiles(t *testing.T) {
	_, err := loadFixture(t, "bad-duplicate")
	if err == nil {
		t.Fatal("Load accepted the same ID in two files")
	}
	for _, want := range []string{"acme/Small-1B-Instruct", "acme.yaml", "other.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't name %q", err, want)
		}
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error %v isn't ErrInvalid", err)
	}
}

func TestYMLFileIsRefused(t *testing.T) {
	_, err := loadFixture(t, "bad-yml")
	if err == nil {
		t.Fatal("Load ignored a .yml family file")
	}
	for _, want := range []string{"beta.yml", ".yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't name %q", err, want)
		}
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error %v isn't ErrInvalid", err)
	}
}

// TestEmbedHoldsEveryDataFile checks that the binary embeds every file in
// data/, whatever its extension, so a stray .yml reaches Load's guard.
func TestEmbedHoldsEveryDataFile(t *testing.T) {
	onDisk, err := os.ReadDir("data")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, d := range onDisk {
		// go:embed leaves out names that start with . or _.
		if !strings.HasPrefix(d.Name(), ".") && !strings.HasPrefix(d.Name(), "_") {
			want = append(want, d.Name())
		}
	}
	embedded, err := fs.ReadDir(dataFS, "data")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(embedded))
	for _, d := range embedded {
		got = append(got, d.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("embedded data/ holds %v, want every file on disk: %v", got, want)
	}

	// The embedded files plus a stray .yml, as the binary would hold them.
	withYML := fstest.MapFS{"stray.yml": {Data: []byte("family: stray\n")}}
	for _, name := range got {
		raw, readErr := fs.ReadFile(dataFS, "data/"+name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		withYML[name] = &fstest.MapFile{Data: raw}
	}
	if _, err := Load(withYML); err == nil || !strings.Contains(err.Error(), "stray.yml") {
		t.Errorf("Load of the embedded files plus stray.yml: %v, want an error naming stray.yml", err)
	}
}

func TestGoodEntriesValidate(t *testing.T) {
	for _, e := range mustLoadGood(t).Entries() {
		if err := Validate(e); err != nil {
			t.Errorf("Validate(%s): %v", e.ID, err)
		}
	}
}

func TestValidateEntry(t *testing.T) {
	r := mustLoadGood(t)
	entry := func(id string) Entry {
		e, ok := r.Entry(id)
		if !ok {
			t.Fatalf("fixture has no %s", id)
		}
		return e
	}
	chat := func() Entry { return entry("acme/Small-1B-Instruct") }

	cases := []struct {
		name  string
		entry func() Entry
		want  string
	}{
		{"revision main", func() Entry { e := chat(); e.Revision = "main"; return e }, "revision"},
		{"quantization revision main", func() Entry { e := chat(); e.Quantizations[1].Revision = "main"; return e }, "revision"},
		{"unknown license", func() Entry { e := chat(); e.License = License{ID: "wtfpl", Commercial: true}; return e }, "license"},
		{"commercial research license", func() Entry {
			e := chat()
			e.License = License{ID: "mistral-mrl", Commercial: true}
			return e
		}, "commercial"},
		{"gguf without file", func() Entry { e := chat(); e.Quantizations[1].File = ""; return e }, "file"},
		{"file on a vllm quantization", func() Entry { e := chat(); e.Quantizations[0].File = "model.gguf"; return e }, "file"},
		{"tei serving chat", func() Entry {
			e := chat()
			e.Quantizations = []Quantization{{ID: "native", Engine: EngineTEI, WeightBytes: 1}}
			return e
		}, "tei can't serve chat"},
		{"engine doesn't match id", func() Entry { e := chat(); e.Quantizations[0].Engine = EngineLlamaCPP; return e }, "engine"},
		{"embedding task without block", func() Entry {
			e := entry("acme/Embed-Small")
			e.Embedding = nil
			return e
		}, "embedding"},
		{"embedding block on a chat model", func() Entry {
			e := chat()
			e.Embedding = &Embedding{Dimensions: 384, MaxInputTokens: 512}
			return e
		}, "embedding"},
		{"em dash in summary", func() Entry { e := chat(); e.Summary = "A small model \u2014 fast."; return e }, "dash"},
		{"en dash in title", func() Entry { e := chat(); e.Title = "Acme 1\u20132B"; return e }, "dash"},
		{"trust_remote_code outside the allowlist", func() Entry { e := chat(); e.TrustRemoteCode = true; return e }, "trust_remote_code"},
		{"MLA without rope_head_dim", func() Entry {
			e := entry("beta/Reasoner-16B")
			e.Architecture.RopeHeadDim = 0
			return e
		}, "rope_head_dim"},
		{"duplicate quantization", func() Entry {
			e := chat()
			e.Quantizations[1] = e.Quantizations[0]
			return e
		}, "duplicate quantization"},
		{"bad id", func() Entry { e := chat(); e.ID = "no-slash"; return e }, "id"},
		{"no tasks", func() Entry { e := chat(); e.Tasks = nil; return e }, "task"},
		{"duplicate task", func() Entry { e := chat(); e.Tasks = []Task{TaskChat, TaskChat}; return e }, "task"},
		{"zero parameters", func() Entry { e := chat(); e.Parameters = 0; return e }, "parameters"},
		{"zero context", func() Entry { e := chat(); e.ContextLength = 0; return e }, "context_length"},
		{"more kv heads than attention heads", func() Entry { e := chat(); e.Architecture.KVHeads = 64; return e }, "kv_heads"},
		{"no kv heads on a chat model", func() Entry { e := chat(); e.Architecture.KVHeads = 0; return e }, "kv_heads"},
		{"pooling model without layers", func() Entry {
			e := entry("acme/Embed-Small")
			e.Architecture.Layers = 0
			return e
		}, "layers"},
		{"unknown quantization", func() Entry { e := chat(); e.Quantizations[0].ID = "int3"; return e }, "quantization"},
		{"zero weight bytes", func() Entry { e := chat(); e.Quantizations[0].WeightBytes = 0; return e }, "weight_bytes"},
		{"no quantizations", func() Entry { e := chat(); e.Quantizations = nil; return e }, "quantization"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.entry()
			err := Validate(e)
			if err == nil {
				t.Fatalf("Validate accepted it, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't contain %q", err, tc.want)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v isn't ErrInvalid", err)
			}
			for _, line := range strings.Split(err.Error(), "\n") {
				if !strings.HasPrefix(line, e.ID+": ") {
					t.Errorf("problem %q doesn't start with the entry ID", line)
				}
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	e, _ := mustLoadGood(t).Entry("acme/Small-1B-Instruct")
	e.Revision = "main"
	e.Parameters = 0
	e.Summary = "Fast \u2014 small."
	err := Validate(e)
	if err == nil {
		t.Fatal("Validate accepted three problems")
	}
	if n := len(strings.Split(err.Error(), "\n")); n != 3 {
		t.Errorf("got %d problems, want 3:\n%v", n, err)
	}
}

func TestTrustRemoteCodeAllowlist(t *testing.T) {
	e, _ := mustLoadGood(t).Entry("acme/Small-1B-Instruct")
	e.ID = "microsoft/Phi-3.5-vision-instruct"
	e.TrustRemoteCode = true
	if err := Validate(e); err != nil {
		t.Fatalf("Validate refused an allowlisted entry: %v", err)
	}
}

func TestWeightsRepo(t *testing.T) {
	e, _ := mustLoadGood(t).Entry("acme/Small-1B-Instruct")

	bf16, _ := e.Quantization("bf16")
	if repo, rev := bf16.WeightsRepo(e); repo != e.ID || rev != e.Revision {
		t.Errorf("bf16 WeightsRepo = %q, %q; want %q, %q", repo, rev, e.ID, e.Revision)
	}

	gguf, _ := e.Quantization("gguf-q4_k_m")
	if repo, rev := gguf.WeightsRepo(e); repo != gguf.Repo || rev != gguf.Revision {
		t.Errorf("gguf WeightsRepo = %q, %q; want %q, %q", repo, rev, gguf.Repo, gguf.Revision)
	}
	if gguf.Repo == "" || gguf.Repo == e.ID {
		t.Fatalf("fixture gguf repo %q should differ from the entry", gguf.Repo)
	}
}

func TestLicenses(t *testing.T) {
	want := []string{"apache-2.0", "mit", "llama3.1", "llama3.2", "llama3.3", "llama4", "gemma", "qwen", "qwen-research", "deepseek", "mistral-mrl", "mistral-mnpl"}
	got := Licenses()
	if len(got) != len(want) {
		t.Fatalf("got %d licenses, want %d", len(got), len(want))
	}
	nonCommercial := map[string]bool{"qwen-research": true, "mistral-mrl": true, "mistral-mnpl": true}
	for i, l := range got {
		if l.ID != want[i] {
			t.Errorf("license %d = %q, want %q", i, l.ID, want[i])
		}
		if l.Commercial == nonCommercial[l.ID] {
			t.Errorf("%s Commercial = %v", l.ID, l.Commercial)
		}
		if !strings.HasPrefix(l.URL, "https://") || l.Title == "" {
			t.Errorf("%s has title %q, URL %q", l.ID, l.Title, l.URL)
		}
	}
	got[0].ID = "changed"
	if Licenses()[0].ID != "apache-2.0" {
		t.Error("changing the returned slice changed Licenses()")
	}
}

func TestDefaultLoads(t *testing.T) {
	if Default() == nil {
		t.Fatal("Default() is nil")
	}
	for _, e := range Default().Entries() {
		if err := Validate(e); err != nil {
			t.Errorf("Validate(%s): %v", e.ID, err)
		}
	}
}
