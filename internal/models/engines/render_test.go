package engines

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/models/registry"
)

const (
	llamaRevision = "0e9e39f249a16976918f6564b8830bc894c89659"
	fp8Revision   = "8c670bcdb23f58a977e1440354beb7c3e455961d"
)

// llama8B is Llama 3.1 8B Instruct as the registry will hold it once it's
// pinned. It's gated, so the embedded registry doesn't have it yet.
func llama8B() registry.Entry {
	return registry.Entry{
		ID:            "meta-llama/Llama-3.1-8B-Instruct",
		Title:         "Llama 3.1 8B Instruct",
		Revision:      llamaRevision,
		Gated:         true,
		Tasks:         []registry.Task{registry.TaskChat, registry.TaskCode},
		ContextLength: 131072,
		ToolParser:    "llama3_json",
		Quantizations: []registry.Quantization{
			{ID: "bf16", Engine: registry.EngineVLLM, WeightBytes: 16060522496},
			{ID: "fp8", Engine: registry.EngineVLLM, Repo: "RedHatAI/Meta-Llama-3.1-8B-Instruct-FP8", Revision: fp8Revision, WeightBytes: 9083725488},
		},
	}
}

// curated returns an entry and one of its quantizations from the embedded
// registry.
func curated(t *testing.T, id, quantization string) (registry.Entry, registry.Quantization) {
	t.Helper()
	e, ok := registry.Default().Entry(id)
	if !ok {
		t.Fatalf("registry has no %s", id)
	}
	q, ok := e.Quantization(quantization)
	if !ok {
		t.Fatalf("%s has no %s quantization", id, quantization)
	}
	return e, q
}

func TestVLLMArgs(t *testing.T) {
	e := llama8B()
	q, _ := e.Quantization("fp8")
	got, err := Args(Spec{Entry: e, Quantization: q, ServedName: e.ID, MaxModelLen: 16384, GPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"RedHatAI/Meta-Llama-3.1-8B-Instruct-FP8",
		"--revision=" + fp8Revision,
		"--tokenizer-revision=" + fp8Revision,
		"--served-model-name=meta-llama/Llama-3.1-8B-Instruct",
		"--host=0.0.0.0",
		"--port=8000",
		"--max-model-len=16384",
		"--gpu-memory-utilization=0.90",
		"--tensor-parallel-size=2",
		"--enable-auto-tool-choice",
		"--tool-call-parser=llama3_json",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Args =\n%q\nwant\n%q", got, want)
	}

	// Trust, the reasoning parser and the quantization's own args follow
	// the tool parser, in that order.
	e.TrustRemoteCode = true
	e.ReasoningParser = "deepseek_r1"
	q.Args = []string{"--kv-cache-dtype=fp8"}
	got, err = Args(Spec{Entry: e, Quantization: q, ServedName: e.ID, MaxModelLen: 16384, GPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []string{
		"--trust-remote-code",
		"--enable-auto-tool-choice",
		"--tool-call-parser=llama3_json",
		"--reasoning-parser=deepseek_r1",
		"--kv-cache-dtype=fp8",
	}
	if tail := got[9:]; !slices.Equal(tail, wantTail) {
		t.Errorf("Args after --tensor-parallel-size =\n%q\nwant\n%q", tail, wantTail)
	}
}

func TestVLLMPoolingArgs(t *testing.T) {
	e, q := curated(t, "BAAI/bge-m3", "bf16")
	// A parser on a pooling entry is ignored: there's nothing to call.
	e.ToolParser = "hermes"
	e.ReasoningParser = "deepseek_r1"
	got, err := Args(Spec{Entry: e, Quantization: q, ServedName: "bge", MaxModelLen: 4096, GPUs: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"BAAI/bge-m3",
		"--revision=" + e.Revision,
		"--tokenizer-revision=" + e.Revision,
		"--served-model-name=bge",
		"--host=0.0.0.0",
		"--port=8000",
		"--max-model-len=8192", // context_length 8194, capped
		"--gpu-memory-utilization=0.90",
		"--tensor-parallel-size=1",
		"--runner=pooling",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Args =\n%q\nwant\n%q", got, want)
	}

	// Below the cap, the entry's own context length.
	e.ContextLength = 512
	got, err = Args(Spec{Entry: e, Quantization: q, ServedName: "bge", MaxModelLen: 4096, GPUs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--max-model-len=512") {
		t.Errorf("Args = %q, want --max-model-len=512", got)
	}
}

func TestTEIArgs(t *testing.T) {
	e, q := curated(t, "BAAI/bge-m3", "native")
	got, err := Args(Spec{Entry: e, Quantization: q, ServedName: "BAAI/bge-m3", MaxModelLen: 8192})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--model-id=BAAI/bge-m3",
		"--revision=" + e.Revision,
		"--served-model-name=BAAI/bge-m3",
		"--hostname=0.0.0.0",
		"--port=8000",
		"--huggingface-hub-cache=/cache",
		"--max-client-batch-size=64",
		"--auto-truncate",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Args =\n%q\nwant\n%q", got, want)
	}

	q.Args = []string{"--pooling=cls"}
	got, err = Args(Spec{Entry: e, Quantization: q, ServedName: "BAAI/bge-m3", MaxModelLen: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, slices.Concat(want, []string{"--pooling=cls"})) {
		t.Errorf("Args = %q, want the quantization's args last", got)
	}
}

func TestLlamaCPPArgs(t *testing.T) {
	e, q := curated(t, "Qwen/Qwen2.5-7B-Instruct", "gguf-q4_k_m")
	spec := Spec{Entry: e, Quantization: q, ServedName: "qwen-7b", MaxModelLen: 8192}
	cpu, err := Args(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--model-url=https://huggingface.co/bartowski/Qwen2.5-7B-Instruct-GGUF/resolve/8911e8a47f92bac19d6f5c64a2e2095bd2f7d031/Qwen2.5-7B-Instruct-Q4_K_M.gguf",
		"--model=/cache/8911e8a47f92-Qwen2.5-7B-Instruct-Q4_K_M.gguf",
		"--alias=qwen-7b",
		"--host=0.0.0.0",
		"--port=8000",
		"--ctx-size=8192",
		"--jinja",
		"--n-gpu-layers=0",
	}
	if !slices.Equal(cpu, want) {
		t.Errorf("CPU Args =\n%q\nwant\n%q", cpu, want)
	}

	spec.OnGPU, spec.GPUs = true, 1
	gpu, err := Args(spec)
	if err != nil {
		t.Fatal(err)
	}
	want[len(want)-1] = "--n-gpu-layers=999"
	if !slices.Equal(gpu, want) {
		t.Errorf("GPU Args =\n%q\nwant\n%q", gpu, want)
	}
}

func TestArgsRefuseUnknownFlags(t *testing.T) {
	e, q := curated(t, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	q.Args = []string{"--made-up=1"}
	_, err := Args(Spec{Entry: e, Quantization: q, ServedName: "qwen", MaxModelLen: 4096, GPUs: 1})
	if err == nil || !strings.Contains(err.Error(), "--made-up") {
		t.Errorf("Args with --made-up=1: err = %v, want one naming --made-up", err)
	}

	q.Args = []string{"--max-num-seqs=8\n--trust-remote-code"}
	if _, err = Args(Spec{Entry: e, Quantization: q, ServedName: "qwen", MaxModelLen: 4096, GPUs: 1}); err == nil {
		t.Error("Args with a newline in an arg: no error")
	}

	q.Args = nil
	if _, err = Args(Spec{Entry: e, Quantization: q, ServedName: "qwen\n--trust-remote-code", MaxModelLen: 4096, GPUs: 1}); err == nil {
		t.Error("Args with a newline in the served name: no error")
	}

	// A flag llama.cpp has but TEI doesn't is still refused on TEI.
	te, tq := curated(t, "BAAI/bge-m3", "native")
	tq.Args = []string{"--jinja"}
	_, err = Args(Spec{Entry: te, Quantization: tq, ServedName: "bge", MaxModelLen: 8192})
	if err == nil || !strings.Contains(err.Error(), "--jinja") {
		t.Errorf("TEI Args with --jinja: err = %v, want one naming --jinja", err)
	}

	if _, err := Args(Spec{Entry: e, Quantization: registry.Quantization{ID: "bf16", Engine: registry.EngineVLLM}, ServedName: "qwen", MaxModelLen: 4096}); err == nil {
		t.Error("vLLM Args with no GPUs: no error")
	}
}

const wantGuardConfig = `{
	admin off
	auto_https off
}
:8080 {
	@health path /health
	handle @health {
		reverse_proxy {$LOOMPLANE_ENGINE_ADDR}
	}
	@key header Authorization "Bearer {$LOOMPLANE_MODEL_KEY}"
	handle @key {
		reverse_proxy {$LOOMPLANE_ENGINE_ADDR} {
			flush_interval -1
		}
	}
	handle {
		respond "Send the model's API key as a bearer token." 401
	}
}
`

func TestGuardConfigHasNoSecret(t *testing.T) {
	got := string(GuardConfig())
	if got != wantGuardConfig {
		t.Errorf("GuardConfig() =\n%s\nwant\n%s", got, wantGuardConfig)
	}
	if !strings.Contains(got, "{$LOOMPLANE_MODEL_KEY}") {
		t.Error("GuardConfig() doesn't read the key from LOOMPLANE_MODEL_KEY")
	}
	if strings.Contains(got, "lpm_") {
		t.Error("GuardConfig() holds a model key")
	}
	if strings.ContainsAny(got, "\u2014\u2013") {
		t.Error("GuardConfig() has an em or en dash")
	}
}

// The guard sees the key on every request, so it must never log one: no
// access log and no admin endpoint that could turn one on.
func TestGuardConfigNeverLogsTheKey(t *testing.T) {
	got := string(GuardConfig())
	if !regexp.MustCompile(`(?m)^\s*admin off$`).MatchString(got) {
		t.Error("GuardConfig() leaves the admin endpoint on")
	}
	if regexp.MustCompile(`(?m)^\s*log(_[a-z_]+)?(\s|$)`).MatchString(got) {
		t.Errorf("GuardConfig() has a log directive:\n%s", got)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	r := registry.Default()
	tests := []struct {
		id, quantization string
		gpus             int
		onGPU            bool
		want             map[string]string
	}{
		{
			id: "Qwen/Qwen2.5-14B-Instruct", quantization: "bf16", gpus: 2,
			want: map[string]string{
				"entry": "Qwen/Qwen2.5-14B-Instruct", "model": "Qwen/Qwen2.5-14B-Instruct",
				"revision": "cf98f3b3bbb457ad9e2bb7baf9a0125b6b88caa8", "quantization": "bf16",
				"served_name": "served", "max_model_len": "16384",
				"cache_mb": "35840", // ceil(1.25 x 29540134000 / 2^30) = 35 GiB
			},
		},
		{
			id: "Qwen/Qwen2.5-7B-Instruct", quantization: "awq-int4", gpus: 1,
			want: map[string]string{
				"entry": "Qwen/Qwen2.5-7B-Instruct", "model": "Qwen/Qwen2.5-7B-Instruct-AWQ",
				"revision": "b25037543e9394b818fdfca67ab2a00ecc7dd641", "quantization": "awq-int4",
				"served_name": "served", "max_model_len": "16384", "cache_mb": "20480",
			},
		},
		{
			id: "Qwen/Qwen2.5-7B-Instruct", quantization: "gguf-q4_k_m", gpus: 1, onGPU: true,
			want: map[string]string{
				"entry": "Qwen/Qwen2.5-7B-Instruct", "model": "bartowski/Qwen2.5-7B-Instruct-GGUF",
				"revision": "8911e8a47f92bac19d6f5c64a2e2095bd2f7d031", "quantization": "gguf-q4_k_m",
				"file":        "Qwen2.5-7B-Instruct-Q4_K_M.gguf",
				"served_name": "served", "max_model_len": "16384", "cache_mb": "20480",
			},
		},
		{id: "Qwen/Qwen2.5-7B-Instruct", quantization: "gguf-q8_0"},
		{id: "BAAI/bge-m3", quantization: "native"},
	}
	for _, tt := range tests {
		t.Run(tt.id+"/"+tt.quantization, func(t *testing.T) {
			e, q := curated(t, tt.id, tt.quantization)
			s := Spec{Entry: e, Quantization: q, ServedName: "served", MaxModelLen: 16384, GPUs: tt.gpus, OnGPU: tt.onGPU}
			settings := Settings(s)
			if tt.want != nil && !reflect.DeepEqual(settings, tt.want) {
				t.Errorf("Settings =\n%v\nwant\n%v", settings, tt.want)
			}
			back, err := FromSettings(context.Background(), r, nil, "org_1", settings, tt.gpus, tt.onGPU)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, s) {
				t.Errorf("FromSettings(Settings(s)) =\n%+v\nwant\n%+v", back, s)
			}
			if _, err := Args(back); err != nil {
				t.Errorf("Args: %v", err)
			}
		})
	}
}

func TestSettingsMatchTheKindPatterns(t *testing.T) {
	patterns := map[string]*regexp.Regexp{
		"entry":         regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}|hf:[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}@[0-9a-f]{12})$`),
		"model":         regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`),
		"revision":      regexp.MustCompile(`^[0-9a-f]{40}$`),
		"quantization":  regexp.MustCompile(`^[a-z0-9_-]{1,32}$`),
		"served_name":   regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`),
		"max_model_len": regexp.MustCompile(`^[1-9][0-9]{2,6}$`),
		"file":          regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}\.gguf$`),
		"cache_mb":      regexp.MustCompile(`^[1-9][0-9]{3,7}$`),
	}
	entries := append(registry.Default().Entries(), customChat())
	for _, e := range entries {
		served, _ := weightsRepo(e, registry.Quantization{}) // the repo, for a custom entry too
		for _, q := range e.Quantizations {
			for name, value := range Settings(Spec{Entry: e, Quantization: q, ServedName: served, MaxModelLen: 4096}) {
				p, ok := patterns[name]
				if !ok {
					t.Fatalf("Settings sets %q, which no model kind takes", name)
				}
				if !p.MatchString(value) {
					t.Errorf("%s %s: %s %q doesn't match %s", e.ID, q.ID, name, value, p)
				}
			}
		}
	}
}

func TestFromSettingsRefusesWhatTheRegistryDoesntPin(t *testing.T) {
	r := registry.Default()
	e, q := curated(t, "Qwen/Qwen2.5-7B-Instruct", "gguf-q4_k_m")
	good := Settings(Spec{Entry: e, Quantization: q, ServedName: "qwen", MaxModelLen: 8192})
	tests := map[string]struct {
		change map[string]string
		onGPU  bool
		want   string
	}{
		"unknown entry":        {change: map[string]string{"entry": "acme/nothing"}, want: "acme/nothing"},
		"unknown quantization": {change: map[string]string{"quantization": "fp8"}, want: "fp8"},
		"another repo":         {change: map[string]string{"model": "evil/Qwen2.5-7B-Instruct-GGUF"}, want: "evil/Qwen2.5-7B-Instruct-GGUF"},
		"another revision":     {change: map[string]string{"revision": strings.Repeat("a", 40)}, want: strings.Repeat("a", 40)},
		"another file":         {change: map[string]string{"file": "Qwen2.5-7B-Instruct-Q2_K.gguf"}, want: "Q2_K"},
		"no served name":       {change: map[string]string{"served_name": ""}, want: "served_name"},
		"bad context length":   {change: map[string]string{"max_model_len": "lots"}, want: "max_model_len"},
		"no entry":             {change: map[string]string{"entry": ""}, want: "entry"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			settings := make(map[string]string, len(good))
			for k, v := range good {
				settings[k] = v
			}
			for k, v := range tt.change {
				settings[k] = v
			}
			_, err := FromSettings(context.Background(), r, nil, "org_1", settings, 0, tt.onGPU)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}

	// A GPU llama.cpp kind can't run a vLLM quantization.
	e, q = curated(t, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	if _, err := FromSettings(context.Background(), r, nil, "org_1", Settings(Spec{Entry: e, Quantization: q, ServedName: "qwen", MaxModelLen: 8192}), 1, true); err == nil {
		t.Error("FromSettings with onGPU for a vLLM quantization: no error")
	}
}

const customRevision = "4f6a1c2b9d8e7f60a1b2c3d4e5f60718293a4b5c"

// customChat is an entry built from a pasted repo, stored under its
// custom ID. Its quantizations name no repo of their own.
func customChat() registry.Entry {
	return registry.Entry{
		ID:            CustomEntryID("acme/chat-7b", customRevision),
		Title:         "chat-7b",
		Revision:      customRevision,
		Tasks:         []registry.Task{registry.TaskChat},
		ContextLength: 32768,
		Quantizations: []registry.Quantization{
			{ID: "bf16", Engine: registry.EngineVLLM, WeightBytes: 15231271888},
			{ID: "gguf-q4_k_m", Engine: registry.EngineLlamaCPP, File: "chat-7b-Q4_K_M.gguf", WeightBytes: 4683074240},
		},
	}
}

var errNoCustomEntry = errors.New("no such custom entry")

// fakeCustom holds custom entries by organization and then ID.
type fakeCustom map[string]map[string]registry.Entry

func (f fakeCustom) Custom(_ context.Context, orgID, entryID string) (registry.Entry, error) {
	e, ok := f[orgID][entryID]
	if !ok {
		return registry.Entry{}, errNoCustomEntry
	}
	return e, nil
}

func TestCustomEntryID(t *testing.T) {
	if got, want := CustomEntryID("acme/chat-7b", customRevision), "hf:acme/chat-7b@4f6a1c2b9d8e"; got != want {
		t.Errorf("CustomEntryID = %q, want %q", got, want)
	}
}

func TestFromSettingsReadsCustomEntries(t *testing.T) {
	ctx := context.Background()
	r := registry.Default()
	e := customChat()
	custom := fakeCustom{"org_1": {e.ID: e}}

	for _, tt := range []struct {
		quantization string
		gpus         int
		onGPU        bool
		wantFirst    string
	}{
		{"bf16", 1, false, "acme/chat-7b"},
		{"gguf-q4_k_m", 1, true, "--model-url=https://huggingface.co/acme/chat-7b/resolve/" + customRevision + "/chat-7b-Q4_K_M.gguf"},
	} {
		q, _ := e.Quantization(tt.quantization)
		s := Spec{Entry: e, Quantization: q, ServedName: "chat", MaxModelLen: 8192, GPUs: tt.gpus, OnGPU: tt.onGPU}
		settings := Settings(s)
		if settings["entry"] != "hf:acme/chat-7b@4f6a1c2b9d8e" || settings["model"] != "acme/chat-7b" || settings["revision"] != customRevision {
			t.Errorf("%s: Settings = %v, want entry hf:acme/chat-7b@4f6a1c2b9d8e, model acme/chat-7b and the full revision", tt.quantization, settings)
		}
		back, err := FromSettings(ctx, r, custom, "org_1", settings, tt.gpus, tt.onGPU)
		if err != nil {
			t.Fatalf("%s: %v", tt.quantization, err)
		}
		if !reflect.DeepEqual(back, s) {
			t.Errorf("%s: FromSettings(Settings(s)) =\n%+v\nwant\n%+v", tt.quantization, back, s)
		}
		args, err := Args(back)
		if err != nil {
			t.Fatalf("%s: Args: %v", tt.quantization, err)
		}
		if args[0] != tt.wantFirst {
			t.Errorf("%s: first arg %q, want %q", tt.quantization, args[0], tt.wantFirst)
		}
	}

	q, _ := e.Quantization("bf16")
	settings := Settings(Spec{Entry: e, Quantization: q, ServedName: "chat", MaxModelLen: 8192, GPUs: 1})

	// Another organization's entry is as good as missing.
	if _, err := FromSettings(ctx, r, custom, "org_2", settings, 1, false); !errors.Is(err, errNoCustomEntry) {
		t.Errorf("FromSettings for another organization: err = %v, want errNoCustomEntry", err)
	}

	missing := map[string]string{}
	for k, v := range settings {
		missing[k] = v
	}
	missing["entry"] = CustomEntryID("acme/other", customRevision)
	if _, err := FromSettings(ctx, r, custom, "org_1", missing, 1, false); !errors.Is(err, errNoCustomEntry) {
		t.Errorf("FromSettings for a missing custom entry: err = %v, want errNoCustomEntry", err)
	}

	if _, err := FromSettings(ctx, r, nil, "org_1", settings, 1, false); err == nil {
		t.Error("FromSettings for a custom entry with no CustomEntries: no error")
	}

	// The registry never answers for an "hf:" ID, even when the repo is in it.
	if _, err := FromSettings(ctx, r, fakeCustom{}, "org_1", map[string]string{
		"entry": CustomEntryID("Qwen/Qwen2.5-7B-Instruct", "a09a35458c702b33eeacc393d103063234e8bc28"), "model": "Qwen/Qwen2.5-7B-Instruct",
		"revision": "a09a35458c702b33eeacc393d103063234e8bc28", "quantization": "bf16", "served_name": "q", "max_model_len": "4096",
	}, 1, false); !errors.Is(err, errNoCustomEntry) {
		t.Errorf("FromSettings for an hf: ID of a registry repo: err = %v, want errNoCustomEntry", err)
	}
}

func TestOnlyRegistryEntriesRunRemoteCode(t *testing.T) {
	ctx := context.Background()
	r := registry.Default()

	// Through a quantization's extra arguments, on any entry.
	e, q := curated(t, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	for _, arg := range []string{"--trust-remote-code", "--trust-remote-code=true"} {
		q.Args = []string{"--max-num-seqs=8", arg}
		_, err := Args(Spec{Entry: e, Quantization: q, ServedName: "qwen", MaxModelLen: 4096, GPUs: 1})
		if err == nil || !strings.Contains(err.Error(), "--trust-remote-code") {
			t.Errorf("Args with %s in the quantization args: err = %v, want one naming --trust-remote-code", arg, err)
		}
	}

	// A pasted repo asking for it, in Args and in FromSettings.
	ce := customChat()
	ce.TrustRemoteCode = true
	cq, _ := ce.Quantization("bf16")
	s := Spec{Entry: ce, Quantization: cq, ServedName: "chat", MaxModelLen: 8192, GPUs: 1}
	if _, err := Args(s); err == nil || !strings.Contains(err.Error(), "only registry entries") {
		t.Errorf("Args for a custom entry with trust_remote_code: err = %v, want a refusal", err)
	}
	custom := fakeCustom{"org_1": {ce.ID: ce}}
	if _, err := FromSettings(ctx, r, custom, "org_1", Settings(s), 1, false); err == nil || !strings.Contains(err.Error(), "only registry entries") {
		t.Errorf("FromSettings for a custom entry with trust_remote_code: err = %v, want a refusal", err)
	}

	// A registry entry that sets trust_remote_code still gets the flag.
	var trusted *registry.Entry
	for _, re := range r.Entries() {
		if re.TrustRemoteCode {
			trusted = &re
			break
		}
	}
	if trusted == nil {
		t.Fatal("the registry has no entry with trust_remote_code to check")
	}
	for _, tq := range trusted.Quantizations {
		if tq.Engine != registry.EngineVLLM {
			continue
		}
		args, err := Args(Spec{Entry: *trusted, Quantization: tq, ServedName: "m", MaxModelLen: 4096, GPUs: 1})
		if err != nil {
			t.Fatalf("%s %s: %v", trusted.ID, tq.ID, err)
		}
		if !slices.Contains(args, "--trust-remote-code") {
			t.Errorf("%s %s: Args = %q, want --trust-remote-code", trusted.ID, tq.ID, args)
		}
		break
	}
}

func TestFromSettingsRefusesContextBeyondTheModel(t *testing.T) {
	ctx := context.Background()
	r := registry.Default()
	e, q := curated(t, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	settings := Settings(Spec{Entry: e, Quantization: q, ServedName: "qwen", MaxModelLen: e.ContextLength})
	if _, err := FromSettings(ctx, r, nil, "org_1", settings, 1, false); err != nil {
		t.Fatalf("FromSettings at the model's context: %v", err)
	}
	settings["max_model_len"] = strconv.Itoa(e.ContextLength + 1)
	_, err := FromSettings(ctx, r, nil, "org_1", settings, 1, false)
	if err == nil || !strings.Contains(err.Error(), "max_model_len") {
		t.Errorf("FromSettings one token past the model's context: err = %v, want one naming max_model_len", err)
	}

	// A pooling model is served at min(context, 8192) whatever is stored.
	pe, pq := curated(t, "BAAI/bge-m3", "native")
	pooled := Settings(Spec{Entry: pe, Quantization: pq, ServedName: "bge", MaxModelLen: pe.ContextLength + 1})
	if _, err := FromSettings(ctx, r, nil, "org_1", pooled, 0, false); err != nil {
		t.Errorf("FromSettings for a pooling model past its context: %v", err)
	}
}

// The context window a model is served at comes from the Spec, not the
// stored max_model_len: a pooling model keeps what was asked for in its
// setting, but vLLM serves min(context, 8192).
func TestContextWindowIsWhatIsServed(t *testing.T) {
	ctx := context.Background()
	r := registry.Default()

	pe, pq := curated(t, "BAAI/bge-m3", "bf16") // context_length 8194
	stored := Settings(Spec{Entry: pe, Quantization: pq, ServedName: "bge", MaxModelLen: 32768, GPUs: 1})
	if stored["max_model_len"] != "32768" {
		t.Fatalf("stored max_model_len = %q, want the requested 32768", stored["max_model_len"])
	}
	spec, err := FromSettings(ctx, r, nil, "org_1", stored, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.ContextWindow(); got != 8192 {
		t.Errorf("pooling ContextWindow = %d, want 8192", got)
	}
	args, err := Args(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--max-model-len="+strconv.Itoa(spec.ContextWindow())) {
		t.Errorf("Args = %q, want the window ContextWindow reports", args)
	}

	// A pooling model shorter than the cap is served at its own context,
	// even when less was asked for.
	short := pe
	short.ContextLength = 512
	if got := (Spec{Entry: short, Quantization: pq, MaxModelLen: 256}).ContextWindow(); got != 512 {
		t.Errorf("short pooling ContextWindow = %d, want 512", got)
	}

	// Anything that generates text is served at what was asked for.
	ce, cq := curated(t, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	chat, err := FromSettings(ctx, r, nil, "org_1", Settings(Spec{Entry: ce, Quantization: cq, ServedName: "qwen", MaxModelLen: 16384}), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := chat.ContextWindow(); got != 16384 {
		t.Errorf("chat ContextWindow = %d, want 16384", got)
	}
}

func TestFromSettingsChecksTheCustomPin(t *testing.T) {
	ctx := context.Background()
	r := registry.Default()

	// The entry's revision isn't the one its ID names.
	moved := customChat()
	moved.Revision = strings.Repeat("b", 40)
	mq, _ := moved.Quantization("bf16")
	settings := Settings(Spec{Entry: moved, Quantization: mq, ServedName: "chat", MaxModelLen: 8192, GPUs: 1})
	_, err := FromSettings(ctx, r, fakeCustom{"org_1": {moved.ID: moved}}, "org_1", settings, 1, false)
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Errorf("FromSettings for a custom entry whose revision moved: err = %v, want one about the revision", err)
	}

	// A quantization whose weights live in another repo.
	elsewhere := customChat()
	elsewhere.Quantizations[0].Repo = "evil/chat-7b"
	elsewhere.Quantizations[0].Revision = customRevision
	eq, _ := elsewhere.Quantization("bf16")
	settings = Settings(Spec{Entry: elsewhere, Quantization: eq, ServedName: "chat", MaxModelLen: 8192, GPUs: 1})
	_, err = FromSettings(ctx, r, fakeCustom{"org_1": {elsewhere.ID: elsewhere}}, "org_1", settings, 1, false)
	if err == nil || !strings.Contains(err.Error(), "evil/chat-7b") {
		t.Errorf("FromSettings for a custom entry with weights in another repo: err = %v, want one naming evil/chat-7b", err)
	}

	// Weights in another revision of the same repo.
	other := customChat()
	other.Quantizations[0].Repo = "acme/chat-7b"
	other.Quantizations[0].Revision = strings.Repeat("c", 40)
	oq, _ := other.Quantization("bf16")
	settings = Settings(Spec{Entry: other, Quantization: oq, ServedName: "chat", MaxModelLen: 8192, GPUs: 1})
	_, err = FromSettings(ctx, r, fakeCustom{"org_1": {other.ID: other}}, "org_1", settings, 1, false)
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Errorf("FromSettings for a custom entry with weights at another revision: err = %v, want one about the revision", err)
	}
}

// A mirror is ai.models.huggingface_url when it isn't huggingface.co: the
// endpoint vLLM and TEI get, and llama.cpp's download host.
func TestHubEndpoint(t *testing.T) {
	for url, want := range map[string]string{
		"":                                "",
		"https://huggingface.co":          "",
		"https://huggingface.co/":         "",
		"HTTPS://HuggingFace.co":          "",
		"https://hf-mirror.example.com/":  "https://hf-mirror.example.com",
		" https://hf.internal:8443/hub/ ": "https://hf.internal:8443/hub",
		"http://127.0.0.1:9000":           "http://127.0.0.1:9000",
	} {
		if got := HubEndpoint(url); got != want {
			t.Errorf("HubEndpoint(%q) = %q, want %q", url, got, want)
		}
	}
	for engine, want := range map[registry.Engine]string{
		registry.EngineVLLM: "https://hf.internal", registry.EngineTEI: "https://hf.internal", registry.EngineLlamaCPP: "",
	} {
		s := Spec{Quantization: registry.Quantization{Engine: engine}, Hub: "https://hf.internal"}
		if got := string(Endpoint(s)); got != want {
			t.Errorf("%s endpoint %q, want %q", engine, got, want)
		}
		s.Hub = ""
		if got := Endpoint(s); len(got) != 0 {
			t.Errorf("%s endpoint without a mirror %q", engine, got)
		}
	}
}
