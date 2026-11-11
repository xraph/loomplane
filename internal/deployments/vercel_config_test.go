package deployments

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestVercelUploadIncludesSharedPackagesAndOverridesOnce(t *testing.T) {
	var mu sync.Mutex
	contents := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		sum := sha1.Sum(data)
		if hex.EncodeToString(sum[:]) != r.Header.Get("x-vercel-digest") {
			t.Error("incorrect content digest")
		}
		mu.Lock()
		contents[r.Header.Get("x-vercel-digest")] = data
		mu.Unlock()
	}))
	defer server.Close()
	archive := buildArchive(t, map[string]string{
		"packages/shared/index.ts":  "export const shared = 1",
		"platform/vercel.json":      `{"services":{"web":{"root":"web/"},"api":{"root":"api/"}},"rewrites":[{"source":"/(.*)","destination":{"service":"web"}}]}`,
		"platform/web/package.json": `{}`, "platform/api/main.py": `app = None`,
	})
	build := "custom build"
	job := &Job{Action: planning.Action{Service: &planning.ServiceConfig{Source: "platform", Vercel: &manifest.VercelSettings{Services: true, Root: "platform", ServiceCommands: map[string]manifest.BuildCommands{"web": {Build: &build}}}}},
		Source: func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
		}}
	v := &Vercel{Client: vercel.New(server.URL)}
	files, err := v.upload(t.Context(), vercel.Token{Value: "test"}, job)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("files = %+v", files)
	}
	count := 0
	for _, file := range files {
		if file.File == "platform/vercel.json" {
			count++
			if !bytes.Contains(contents[file.SHA], []byte(`"buildCommand":"custom build"`)) {
				t.Fatal("uploaded original configuration")
			}
		}
	}
	if count != 1 {
		t.Fatalf("config count = %d", count)
	}
}

func TestVercelServicesUsesOneProjectAndRetriesOneDeployment(t *testing.T) {
	h := newNativeHarness(t, "production", "production", false)
	archive := buildArchive(t, map[string]string{"vercel.json": `{"services":{"web":{"root":"web"},"api":{"root":"api"}}}`})
	h.job.Source = func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
	}
	h.job.Action.Service.Vercel = &manifest.VercelSettings{Services: true}
	checkpoint := h.job.Checkpoint
	h.job.Checkpoint = func(raw []byte) error { h.job.Resume = append([]byte(nil), raw...); return checkpoint(raw) }
	first, err := h.exec.Apply(t.Context(), h.job)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.exec.Apply(t.Context(), h.job)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExternalID != second.ExternalID || first.Revision != second.Revision || len(h.api.deploys) != 1 || h.api.created != 1 {
		t.Fatalf("retried group: %+v %+v", first, second)
	}
	if members, ok := h.api.deploys[0]["services"].(map[string]any); !ok || len(members) != 2 {
		t.Fatalf("Vercel did not receive both services: %+v", h.api.deploys[0])
	}
}

func TestVercelServicesConfigPreservesRoutesAndBindings(t *testing.T) {
	build, skip := "pnpm --filter web build", ""
	cfg := &planning.ServiceConfig{Vercel: &manifest.VercelSettings{Services: true, ServiceCommands: map[string]manifest.BuildCommands{"web": {Build: &build}, "api": {Install: &skip}}}}
	raw := []byte(`{"services":{"web":{"root":"apps/web","bindings":[{"service":"api","env":"API_URL"}],"installCommand":"pnpm install"},"api":{"root":"api","framework":"fastapi"}},"rewrites":[{"source":"/(.*)","destination":{"service":"web"}}],"headers":[{"source":"/(.*)","headers":[]}]}`)
	out, err := vercelSourceConfig(raw, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	var services map[string]map[string]json.RawMessage
	if err := json.Unmarshal(got["services"], &services); err != nil {
		t.Fatal(err)
	}
	if string(services["web"]["buildCommand"]) != `"pnpm --filter web build"` || string(services["api"]["installCommand"]) != `""` {
		t.Fatalf("commands: %s", out)
	}
	if string(services["web"]["installCommand"]) != `"pnpm install"` || len(services["web"]["bindings"]) == 0 || len(got["rewrites"]) == 0 || len(got["headers"]) == 0 {
		t.Fatalf("lost repository settings: %s", out)
	}
}

func TestVercelConfigRejectsAmbiguousServices(t *testing.T) {
	cfg := &planning.ServiceConfig{Vercel: &manifest.VercelSettings{Services: true}}
	for _, raw := range []string{"", `null`, `{}`, `{"experimentalServices":{"web":{}}}`, `{"services":{"web":{"root":"../web"}}}`, `{"services":{"web":{"root":"web"}},"buildCommand":"npm run build"}`} {
		if _, err := vercelSourceConfig([]byte(raw), cfg); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	build := "test"
	cfg.Vercel.ServiceCommands = map[string]manifest.BuildCommands{"typo": {Build: &build}}
	if _, err := vercelSourceConfig([]byte(`{"services":{"web":{"root":"web"}}}`), cfg); err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("unknown member: %v", err)
	}
}

func TestVercelStandaloneCommandOverrides(t *testing.T) {
	empty := ""
	cfg := &planning.ServiceConfig{Source: "apps/web", Commands: &manifest.BuildCommands{Install: &empty}}
	out, err := vercelSourceConfig([]byte(`{"buildCommand":"custom build","rewrites":[]}`), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"installCommand":""`) || !strings.Contains(string(out), `"buildCommand":"custom build"`) {
		t.Fatalf("config = %s", out)
	}
	if vercelConfigPath(cfg) != "apps/web/vercel.json" {
		t.Fatal("wrong config root")
	}
}
