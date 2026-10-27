package engines

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xraph/loomplane/internal/models/registry"
)

const digestsPath = "../../recipes/testdata/model-engine-image-digests.json"

type digestsFile struct {
	CapturedAt string                 `json:"captured_at"`
	Images     map[string]pinnedImage `json:"images"`
}

type pinnedImage struct {
	Repository  string            `json:"repository"`
	Tag         string            `json:"tag"`
	IndexDigest string            `json:"index_digest"`
	Digests     map[string]string `json:"digests"`
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func TestDigestsFileIsComplete(t *testing.T) {
	data, err := os.ReadFile(digestsPath)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f digestsFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode %s: %v", digestsPath, err)
	}
	if _, err := time.Parse(time.DateOnly, f.CapturedAt); err != nil {
		t.Errorf("captured_at %q isn't YYYY-MM-DD", f.CapturedAt)
	}

	current := Current()
	want := map[string]struct {
		repository, tag string
		arches          []string
	}{
		"vllm":         {"docker.io/vllm/vllm-openai", "v" + current[registry.EngineVLLM], []string{"amd64"}},
		"tei":          {"ghcr.io/huggingface/text-embeddings-inference", "cpu-" + current[registry.EngineTEI], []string{"amd64", "arm64"}},
		"llamacpp":     {"ghcr.io/ggml-org/llama.cpp", "server-" + current[registry.EngineLlamaCPP], []string{"amd64", "arm64"}},
		"llamacpp-gpu": {"ghcr.io/ggml-org/llama.cpp", "server-cuda-" + current[registry.EngineLlamaCPP], []string{"amd64"}},
		"guard":        {"docker.io/library/caddy", "", []string{"amd64", "arm64"}},
	}
	if got := slices.Sorted(maps.Keys(f.Images)); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("images = %v, want %v", got, slices.Sorted(maps.Keys(want)))
	}
	for name, w := range want {
		img := f.Images[name]
		if img.Repository != w.repository {
			t.Errorf("%s repository = %q, want %q", name, img.Repository, w.repository)
		}
		if w.tag != "" && img.Tag != w.tag {
			t.Errorf("%s tag = %q, want %q (Current())", name, img.Tag, w.tag)
		}
		if img.Tag == "" {
			t.Errorf("%s has no tag", name)
		}
		if !digestPattern.MatchString(img.IndexDigest) {
			t.Errorf("%s index_digest %q isn't a sha256 digest", name, img.IndexDigest)
		}
		if got := slices.Sorted(maps.Keys(img.Digests)); !slices.Equal(got, w.arches) {
			t.Errorf("%s digests for %v, want %v", name, got, w.arches)
		}
		for arch, d := range img.Digests {
			if !digestPattern.MatchString(d) {
				t.Errorf("%s %s digest %q isn't a sha256 digest", name, arch, d)
			}
		}
	}
}

func TestCapturedListsMatchCurrent(t *testing.T) {
	names, err := fs.Glob(testdataFS, "testdata/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	current := Current()
	seen := make([]string, 0, len(names))
	for _, name := range names {
		engine, version, list, err := parseListName(name)
		if err != nil {
			t.Fatal(err)
		}
		if version != current[engine] {
			t.Errorf("%s: version %s, Current() has %s", name, version, current[engine])
		}
		seen = append(seen, string(engine)+" "+list)
	}
	wantSeen := []string{
		"llamacpp flags",
		"tei architectures",
		"vllm architectures", "vllm flags", "vllm reasoning-parsers", "vllm tool-parsers",
	}
	slices.Sort(seen)
	if !slices.Equal(seen, wantSeen) {
		t.Errorf("lists = %v, want %v", seen, wantSeen)
	}

	for _, f := range []string{"--max-model-len", "--served-model-name", "--revision"} {
		if !KnownFlag(registry.EngineVLLM, f) {
			t.Errorf("vllm flags lack %s", f)
		}
	}
	for _, f := range []string{"--ctx-size", "--model-url", "--alias"} {
		if !KnownFlag(registry.EngineLlamaCPP, f) {
			t.Errorf("llamacpp flags lack %s", f)
		}
	}
}

func TestKnownFlag(t *testing.T) {
	cases := []struct {
		engine registry.Engine
		flag   string
		want   bool
	}{
		{registry.EngineVLLM, "--tensor-parallel-size", true},
		{registry.EngineVLLM, "--tool-call-parser", true},
		{registry.EngineVLLM, "--made-up", false},
		{registry.EngineVLLM, "--max-model-len=8192", false}, // the flag alone, never with its value
		{registry.EngineTEI, "--auto-truncate", true},
		{registry.EngineTEI, "--max-model-len", false},
		{registry.EngineLlamaCPP, "--n-gpu-layers", true},
		{registry.EngineLlamaCPP, "--made-up", false},
		{registry.Engine("ollama"), "--port", false},
	}
	for _, c := range cases {
		if got := KnownFlag(c.engine, c.flag); got != c.want {
			t.Errorf("KnownFlag(%s, %q) = %v, want %v", c.engine, c.flag, got, c.want)
		}
	}
}

func TestSupportsArchitecture(t *testing.T) {
	cases := []struct {
		engine registry.Engine
		arch   string
		want   bool
	}{
		{registry.EngineVLLM, "LlamaForCausalLM", true},
		{registry.EngineVLLM, "MadeUpForCausalLM", false},
		{registry.EngineTEI, "BertModel", true},
		{registry.EngineTEI, "XLMRobertaForSequenceClassification", true},
		{registry.EngineTEI, "LlamaForCausalLM", false},
		{registry.EngineTEI, "MistralModel", false}, // TEI needs CUDA for Mistral, and Loomplane runs it on CPU
		{registry.EngineLlamaCPP, "LlamaForCausalLM", false},
	}
	for _, c := range cases {
		if got := SupportsArchitecture(c.engine, c.arch); got != c.want {
			t.Errorf("SupportsArchitecture(%s, %q) = %v, want %v", c.engine, c.arch, got, c.want)
		}
	}
}

func TestParsersAreCaptured(t *testing.T) {
	for _, p := range []string{"hermes", "llama3_json", "mistral"} {
		if !slices.Contains(ToolParsers(), p) {
			t.Errorf("ToolParsers() lacks %q", p)
		}
	}
	for _, p := range []string{"deepseek_r1", "qwen3"} {
		if !slices.Contains(ReasoningParsers(), p) {
			t.Errorf("ReasoningParsers() lacks %q", p)
		}
	}
	got := ToolParsers()
	got[0] = "changed"
	if ToolParsers()[0] == "changed" {
		t.Error("ToolParsers() returns the package's own slice")
	}
}

func TestCurrentFollowsReleases(t *testing.T) {
	current := Current()
	if len(current) != len(Releases()) {
		t.Fatalf("Current() has %d engines, Releases() %d", len(current), len(Releases()))
	}
	for _, r := range Releases() {
		if r.Version == "" || current[r.Engine] != r.Product {
			t.Errorf("release %+v doesn't match Current()[%s] = %q", r, r.Engine, current[r.Engine])
		}
	}
	for _, e := range []registry.Engine{registry.EngineVLLM, registry.EngineTEI, registry.EngineLlamaCPP} {
		if current[e] == "" {
			t.Errorf("Current() has no %s", e)
		}
	}
}

func TestLoadCapturedRefuses(t *testing.T) {
	current := map[registry.Engine]string{
		registry.EngineVLLM:     "0.31.0",
		registry.EngineTEI:      "1.9.4",
		registry.EngineLlamaCPP: "b11580",
	}
	good := func() fstest.MapFS {
		file := func(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte("# image@sha256:00\n" + body)} }
		return fstest.MapFS{
			"vllm-0.31.0-flags.txt":             file("--port\n"),
			"vllm-0.31.0-architectures.txt":     file("LlamaForCausalLM\n"),
			"vllm-0.31.0-tool-parsers.txt":      file("hermes\n"),
			"vllm-0.31.0-reasoning-parsers.txt": file("qwen3\n"),
			"tei-1.9.4-architectures.txt":       file("BertModel\n"),
			"llamacpp-b11580-flags.txt":         file("--ctx-size\n\n# a note\n--alias\n"),
		}
	}
	c, err := loadCaptured(good(), current)
	if err != nil {
		t.Fatalf("good lists: %v", err)
	}
	if got := c[registry.EngineLlamaCPP][listFlags]; !slices.Equal(got, []string{"--alias", "--ctx-size"}) {
		t.Errorf("llamacpp flags = %v, want sorted entries without blank or # lines", got)
	}

	cases := []struct {
		name  string
		edit  func(fstest.MapFS)
		inErr string
	}{
		{"stale version", func(m fstest.MapFS) {
			m["vllm-0.30.0-flags.txt"] = m["vllm-0.31.0-flags.txt"]
			delete(m, "vllm-0.31.0-flags.txt")
		}, "vllm-0.30.0-flags.txt is for vllm 0.30.0, but Loomplane pins 0.31.0"},
		{"missing list", func(m fstest.MapFS) { delete(m, "vllm-0.31.0-tool-parsers.txt") }, "missing vllm-0.31.0-tool-parsers.txt"},
		{"unknown engine", func(m fstest.MapFS) { m["ollama-1.0-flags.txt"] = m["vllm-0.31.0-flags.txt"] }, "ollama-1.0-flags.txt"},
		{"list the engine can't have", func(m fstest.MapFS) { m["tei-1.9.4-flags.txt"] = m["vllm-0.31.0-flags.txt"] }, "tei lists are architectures"},
		{"no header", func(m fstest.MapFS) { m["tei-1.9.4-architectures.txt"] = &fstest.MapFile{Data: []byte("BertModel\n")} }, "the first line must be a # line"},
		{"empty", func(m fstest.MapFS) {
			m["tei-1.9.4-architectures.txt"] = &fstest.MapFile{Data: []byte("# image\n\n")}
		}, "no entries"},
		{"not a flag", func(m fstest.MapFS) {
			m["llamacpp-b11580-flags.txt"] = &fstest.MapFile{Data: []byte("# image\nctx-size\n")}
		}, `"ctx-size" isn't a long flag`},
	}
	for _, c := range cases {
		m := good()
		c.edit(m)
		_, err := loadCaptured(m, current)
		if err == nil || !strings.Contains(err.Error(), c.inErr) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.inErr)
		}
	}
}
