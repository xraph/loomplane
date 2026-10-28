package models

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/security"
)

const qwenSHA = "a09a35458c702b33eeacc393d103063234e8bc28"

// qwenRepo is Qwen/Qwen2.5-7B-Instruct as the Hub has it at qwenSHA,
// with its real config.json.
func qwenRepo(t *testing.T) *hubRepo {
	t.Helper()
	config, err := os.ReadFile("hfhub/testdata/qwen2.5-7b-instruct-config.json")
	if err != nil {
		t.Fatal(err)
	}
	return &hubRepo{
		id: qwen7B, sha: qwenSHA, pipeline: "text-generation", tags: []string{"license:apache-2.0", "conversational"},
		config: string(config), readme: "# Qwen\n",
		files: map[string]int64{
			"config.json": 663, "README.md": 6175, "tokenizer.json": 7031645,
			"model-00001-of-00004.safetensors": 3945441440, "model-00002-of-00004.safetensors": 3864726352,
			"model-00003-of-00004.safetensors": 3864726424, "model-00004-of-00004.safetensors": 3556377672,
		},
	}
}

func anonymous(t *testing.T, requests []string) {
	t.Helper()
	for _, r := range requests {
		if !strings.HasSuffix(r, " anonymous") {
			t.Errorf("request %q carried a token", r)
		}
	}
}

var otherOrg = security.Principal{UserID: "usr_2", OrgID: "org_other"}

func TestInspectBuildsAnUntestedEntry(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, []gpus.GPUTarget{l4Server(true)})
	b.hub.add(qwenRepo(t))

	d, err := b.svc.Inspect(ctx, member, qwen7B, "")
	if err != nil {
		t.Fatal(err)
	}
	id := engines.CustomEntryID(qwen7B, qwenSHA)
	if d.ID != id || d.ID != "hf:Qwen/Qwen2.5-7B-Instruct@a09a35458c70" || d.Revision != qwenSHA {
		t.Errorf("ID %q revision %q", d.ID, d.Revision)
	}
	// Even though the registry lists the same repo as tested or estimated.
	if d.Verification != registry.StatusUntested || len(d.TestedOn) != 0 || d.Summary != hfhub.CustomSummary {
		t.Errorf("verification %s tested on %v summary %q", d.Verification, d.TestedOn, d.Summary)
	}
	if len(d.Quantizations) != 1 || d.Quantizations[0].ID != "bf16" || d.Quantizations[0].Engine != registry.EngineVLLM {
		t.Fatalf("quantizations %+v", d.Quantizations)
	}
	for _, f := range d.Quantizations[0].Fits {
		if f.Status != registry.StatusUntested && f.Status != registry.StatusNoFit {
			t.Errorf("%s: %s", f.GPUKind, f.Status)
		}
	}
	if !is(d.FitsHere, true) || d.HuggingFaceURL != "https://huggingface.co/"+qwen7B || !d.License.Commercial {
		t.Errorf("fits %v url %q license %+v", d.FitsHere, d.HuggingFaceURL, d.License)
	}
	// main was asked for and the commit was pinned, with no token anywhere.
	asked := b.hub.requests()
	if !slices.Equal(asked, []string{
		"GET /api/models/" + qwen7B + "/revision/main anonymous",
		"GET /" + qwen7B + "/resolve/" + qwenSHA + "/config.json anonymous",
	}) {
		t.Errorf("requests %v", asked)
	}
	if b.tokens.reads != 0 {
		t.Errorf("read the workspace token %d times for a public repo", b.tokens.reads)
	}

	// For an hour the workspace finds it by ID, and a second look at the
	// same commit asks Hugging Face nothing.
	got, err := b.svc.Entry(ctx, member, id)
	if err != nil || got.ID != id {
		t.Errorf("Entry after Inspect: %v", err)
	}
	if _, err = b.svc.Inspect(ctx, member, qwen7B, qwenSHA); err != nil || len(b.hub.requests()) != 2 {
		t.Errorf("inspect again: %d requests, %v", len(b.hub.requests()), err)
	}
	// Another workspace never sees it.
	if _, err = b.svc.Entry(ctx, otherOrg, id); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("another workspace: %v", err)
	}
	if _, err = b.svc.Card(ctx, otherOrg, id); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("another workspace's card: %v", err)
	}

	// After an hour it's gone, unless it was deployed, which stores it.
	*b.now = b.now.Add(61 * time.Minute)
	if _, err = b.svc.Entry(ctx, member, id); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("an hour later: %v", err)
	}
	e, err := hfhub.Build(&hfhub.ModelInfo{ID: qwen7B, SHA: qwenSHA, PipelineTag: "text-generation",
		Files: []hfhub.FileInfo{{Name: "model.safetensors", Size: 15231271888}}}, []byte(qwenRepo(t).config))
	if err != nil {
		t.Fatal(err)
	}
	if err = b.custom.PutCustom(ctx, member.OrgID, e); err != nil {
		t.Fatal(err)
	}
	if _, err = b.svc.Entry(ctx, member, id); err != nil {
		t.Errorf("stored: %v", err)
	}
	if _, err = b.svc.Entry(ctx, otherOrg, id); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("stored, from another workspace: %v", err)
	}
}

func TestInspectPinsTheRevision(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	b.hub.add(qwenRepo(t))
	if d, err := b.svc.Inspect(ctx, member, qwen7B, qwenSHA); err != nil || d.Revision != qwenSHA {
		t.Errorf("at the SHA: %v", err)
	}
	other := strings.Repeat("b", 40)
	if _, err := b.svc.Inspect(ctx, member, qwen7B, other); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("at a commit the repo doesn't have: %v", err)
	}
	if _, err := b.svc.Inspect(ctx, member, "acme/missing", ""); !errors.Is(err, ErrEntryNotFound) ||
		!strings.Contains(err.Error(), "Hugging Face has no model acme/missing at main") {
		t.Errorf("missing repo: %v", err)
	}
	for _, bad := range []string{"", "no-slash", "../etc/passwd", "a/b/c"} {
		if _, err := b.svc.Inspect(ctx, member, bad, ""); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("repo %q: %v", bad, err)
		}
	}
}

func TestInspectRefusesRepositoryCode(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	repo := qwenRepo(t)
	repo.id = "acme/foo-7b"
	repo.config = `{"architectures":["FooForCausalLM"],"auto_map":{"AutoModelForCausalLM":"modeling_foo.FooForCausalLM"},
		"num_hidden_layers":4,"num_attention_heads":8,"hidden_size":512,"max_position_embeddings":4096}`
	b.hub.add(repo)
	_, err := b.svc.Inspect(context.Background(), member, "acme/foo-7b", "")
	if !errors.Is(err, hfhub.ErrRefused) ||
		err.Error() != "acme/foo-7b needs code from its repository to run, and Loomplane doesn't run repository code for pasted models." {
		t.Errorf("auto_map: %v", err)
	}
}

func TestInspectRefusesOtherTasks(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	repo := qwenRepo(t)
	repo.id, repo.pipeline = "acme/painter", "text-to-image"
	b.hub.add(repo)
	_, err := b.svc.Inspect(context.Background(), member, "acme/painter", "")
	if !errors.Is(err, hfhub.ErrRefused) ||
		err.Error() != "Loomplane serves chat, vision, embedding, reranking and speech models. acme/painter is text-to-image." {
		t.Errorf("text-to-image: %v", err)
	}
}

func TestInspectReportsGatedAccess(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	repo := qwenRepo(t)
	repo.id, repo.gated = "acme/gated-7b", true
	b.hub.add(repo)

	// No workspace token: say to add one.
	_, err := b.svc.Inspect(ctx, member, "acme/gated-7b", "")
	var gated *GatedError
	if !errors.As(err, &gated) || err.Error() != "acme/gated-7b is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings" {
		t.Errorf("without a token: %v", err)
	}
	anonymous(t, b.hub.requests())

	// A token whose account hasn't accepted the license.
	b.tokens.token, b.tokens.revision = hfToken, 3
	_, err = b.svc.Inspect(ctx, member, "acme/gated-7b", "")
	var access *GatedAccessError
	if !errors.As(err, &access) || !strings.Contains(err.Error(), "hasn't been granted access. Accept its license on Hugging Face") {
		t.Errorf("token without access: %v", err)
	}
	if strings.Contains(err.Error(), hfToken) {
		t.Error("the error holds the token")
	}

	// Granted: the metadata is still read anonymously, and the token goes
	// only to the requests a gated repo needs it for.
	b.hub.grant(hfToken)
	before := len(b.hub.requests())
	d, err := b.svc.Inspect(ctx, member, "acme/gated-7b", "")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Gated || d.Verification != registry.StatusUntested {
		t.Errorf("gated %v verification %s", d.Gated, d.Verification)
	}
	if got := b.hub.requests()[before:]; !slices.Equal(got, []string{
		"GET /api/models/acme/gated-7b/revision/main anonymous",
		"HEAD /acme/gated-7b/resolve/" + qwenSHA + "/config.json token " + hfToken,
		"GET /acme/gated-7b/resolve/" + qwenSHA + "/config.json token " + hfToken,
	}) {
		t.Errorf("requests %v", got)
	}
}

// A private repo answers anonymous requests as if it didn't exist; the
// workspace's token can read it.
func TestInspectReadsAPrivateRepoWithTheToken(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	repo := qwenRepo(t)
	repo.id, repo.private = "acme/private-7b", true
	b.hub.add(repo)
	if _, err := b.svc.Inspect(context.Background(), member, "acme/private-7b", ""); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("without a token: %v", err)
	}
	b.tokens.token = hfToken
	b.hub.grant(hfToken)
	d, err := b.svc.Inspect(context.Background(), member, "acme/private-7b", "")
	if err != nil {
		t.Fatal(err)
	}
	// The engine needs the token to download it, as it does for a gated one.
	if !d.Gated {
		t.Error("a private repo isn't marked as needing the token")
	}
}

func TestInspectNeedsAIEdit(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), viewer, nil)
	b.hub.add(qwenRepo(t))
	if _, err := b.svc.Inspect(context.Background(), member, qwen7B, ""); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("viewer: %v", err)
	}
	if got := b.hub.requests(); len(got) != 0 {
		t.Errorf("a viewer's Inspect asked Hugging Face %v", got)
	}
}

// ggufRepo is a GGUF repo with no config.json, made from Qwen 7B.
func ggufRepo(q4Size int64) *hubRepo {
	return &hubRepo{
		id: "acme/Qwen2.5-7B-Instruct-GGUF", sha: strings.Repeat("c", 40), pipeline: "text-generation",
		tags:  []string{"gguf", "base_model:Qwen/Qwen2.5-7B", "base_model:quantized:" + qwen7B},
		files: map[string]int64{"Qwen2.5-7B-Instruct-Q4_K_M.gguf": q4Size, "README.md": 100},
	}
}

func TestInspectReadsAGGUFRepo(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil, cpuServer())
	b.hub.add(qwenRepo(t))
	b.hub.add(ggufRepo(4683074240))
	d, err := b.svc.Inspect(context.Background(), member, "acme/Qwen2.5-7B-Instruct-GGUF", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Quantizations) != 1 || d.Quantizations[0].ID != "gguf-q4_k_m" || d.Quantizations[0].File != "Qwen2.5-7B-Instruct-Q4_K_M.gguf" {
		t.Fatalf("quantizations %+v", d.Quantizations)
	}
	// The base model's config sizes it.
	if d.ContextLength != 32768 || d.Quantizations[0].MinMemoryGiB != 6 {
		t.Errorf("context %d, min memory %d GiB", d.ContextLength, d.Quantizations[0].MinMemoryGiB)
	}
	if !is(d.FitsHere, true) {
		t.Errorf("4.4 GiB GGUF with a CPU server: fits %v", d.FitsHere)
	}
}

// Ruling M20: llama.cpp on CPU gets a fixed 20480 MB weight cache on
// Kubernetes, so a pasted GGUF too big for it never fits on CPU, whatever
// the memory math says, and the fit says why.
func TestInspectWarnsWhenGGUFOutgrowsTheCPUCache(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil, cpuServer())
	b.hub.add(qwenRepo(t))
	b.hub.add(ggufRepo(17 << 30))
	d, err := b.svc.Inspect(context.Background(), member, "acme/Qwen2.5-7B-Instruct-GGUF", "")
	if err != nil {
		t.Fatal(err)
	}
	fits := d.Quantizations[0].Fits
	cpu := fits[len(fits)-1]
	want := "Qwen2.5-7B-Instruct-Q4_K_M.gguf needs a 22528 MB weight cache, and llama.cpp on CPU gets 20480 MB on Kubernetes. Run it on a GPU instead."
	if cpu.GPUKind != registry.CPUKind || cpu.Fits() || cpu.Reason != want {
		t.Errorf("CPU fit %+v", cpu)
	}
	if !is(d.FitsHere, false) {
		t.Errorf("with only a CPU server: fits %v", d.FitsHere)
	}
	// It can still run on a big enough GPU.
	h100 := slices.IndexFunc(fits, func(f registry.Fit) bool { return f.GPUKind == "nvidia-h100-80gb" })
	if !fits[h100].Fits() {
		t.Errorf("on an H100: %+v", fits[h100])
	}
}

func TestCheckCustom(t *testing.T) {
	e, err := hfhub.Build(&hfhub.ModelInfo{ID: qwen7B, SHA: qwenSHA, PipelineTag: "text-generation",
		Files: []hfhub.FileInfo{{Name: "model.safetensors", Size: 15231271888}}}, []byte(qwenRepo(t).config))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCustom(e); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*registry.Entry){
		"registry ID":      func(e *registry.Entry) { e.ID = qwen7B },
		"another revision": func(e *registry.Entry) { e.Revision = strings.Repeat("d", 40) },
		"a branch":         func(e *registry.Entry) { e.Revision = "main" },
		"remote code":      func(e *registry.Entry) { e.TrustRemoteCode = true },
		"a quantization in another repo": func(e *registry.Entry) {
			e.Quantizations[0].Repo, e.Quantizations[0].Revision = "acme/other", qwenSHA
		},
		// Build never gives a quantization arguments; these would reach
		// the engine's command line.
		"extra arguments": func(e *registry.Entry) { e.Quantizations[0].Args = []string{"--enable-lora"} },
		"a GGUF file name with a query": func(e *registry.Entry) {
			e.Quantizations[0].Engine, e.Quantizations[0].File = registry.EngineLlamaCPP, "m.gguf?x=1"
		},
		"a GGUF file in a folder": func(e *registry.Entry) {
			e.Quantizations[0].Engine, e.Quantizations[0].File = registry.EngineLlamaCPP, "q/m.gguf"
		},
	} {
		bad := e
		bad.Quantizations = slices.Clone(e.Quantizations)
		change(&bad)
		if err := checkCustom(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A gated GGUF-only repo has no config.json, so asking Hugging Face about
// it would say "not found" whoever asked. Access is asked about one of its
// GGUF files instead.
func TestInspectChecksAGatedGGUFRepoByItsFile(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil, cpuServer())
	b.hub.add(qwenRepo(t))
	gguf := ggufRepo(4683074240)
	gguf.gated = true
	b.hub.add(gguf)
	b.tokens.token = hfToken
	b.hub.grant(hfToken)

	d, err := b.svc.Inspect(ctx, member, gguf.id, "")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Gated || len(d.Quantizations) != 1 {
		t.Errorf("gated %v quantizations %+v", d.Gated, d.Quantizations)
	}
	file := "HEAD /" + gguf.id + "/resolve/" + gguf.sha + "/Qwen2.5-7B-Instruct-Q4_K_M.gguf token " + hfToken
	if !slices.Contains(b.hub.requests(), file) {
		t.Errorf("never asked about the GGUF file: %v", b.hub.requests())
	}
}

// A public GGUF of a gated base model needs the workspace's token only to
// read the base model's config.json, and the error says so and names it.
func TestInspectSaysTheTokenIsForTheBaseModel(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil, cpuServer())
	base := qwenRepo(t)
	base.gated = true
	b.hub.add(base)
	b.hub.add(ggufRepo(4683074240))

	_, err := b.svc.Inspect(ctx, member, "acme/Qwen2.5-7B-Instruct-GGUF", "")
	var baseGated *BaseGatedError
	var gated *GatedError
	if !errors.As(err, &baseGated) || !errors.As(err, &gated) || !baseGated.NoToken {
		t.Fatalf("without a token: %v", err)
	}
	want := "acme/Qwen2.5-7B-Instruct-GGUF has no config.json, so Loomplane sizes it from its base model " + qwen7B +
		", which is gated on Hugging Face. The workspace's Hugging Face token is needed only for that, to read " + qwen7B +
		"'s config.json. Add a token that can read " + qwen7B + " in AI settings, then try again"
	if err.Error() != want {
		t.Errorf("without a token:\n got %q\nwant %q", err, want)
	}

	// Another account's access doesn't help the workspace's token.
	b.hub.grant("hf_someoneElsesTokenVALUE")
	b.tokens.token = hfToken
	_, err = b.svc.Inspect(ctx, member, "acme/Qwen2.5-7B-Instruct-GGUF", "")
	if !errors.As(err, &baseGated) || baseGated.NoToken ||
		!strings.Contains(err.Error(), "needed only for that") || !strings.Contains(err.Error(), "Accept "+qwen7B+"'s license") {
		t.Errorf("a token without access: %v", err)
	}
	if strings.Contains(err.Error(), hfToken) {
		t.Error("the error holds the token")
	}

	b.hub.grant(hfToken)
	if _, err = b.svc.Inspect(ctx, member, "acme/Qwen2.5-7B-Instruct-GGUF", ""); err != nil {
		t.Errorf("granted: %v", err)
	}
}

// A GGUF repo's parameter count comes from its base model's safetensors
// when the Hub gives one, and otherwise from what a weight takes in the
// quantization, never half the file's bytes.
func TestInspectCountsAGGUFsParameters(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil, cpuServer())
	b.hub.add(qwenRepo(t))
	b.hub.add(ggufRepo(4683074240))
	d, err := b.svc.Inspect(ctx, member, "acme/Qwen2.5-7B-Instruct-GGUF", "")
	if err != nil {
		t.Fatal(err)
	}
	// 4683074240 bytes at about 0.56 a weight.
	if d.Parameters != 8362632571 {
		t.Errorf("without the base model's count: %d", d.Parameters)
	}

	b = newBrowse(t, browseRegistry(t, ""), developer, nil, cpuServer())
	base := qwenRepo(t)
	base.params = 7615616512
	b.hub.add(base)
	b.hub.add(ggufRepo(4683074240))
	if d, err = b.svc.Inspect(ctx, member, "acme/Qwen2.5-7B-Instruct-GGUF", ""); err != nil || d.Parameters != 7615616512 {
		t.Errorf("with the base model's count: %d, %v", d.Parameters, err)
	}
}
