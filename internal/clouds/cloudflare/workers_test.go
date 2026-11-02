package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkerUploadSendsMultipartModulesNativeBindingsAndMigrations(t *testing.T) {
	c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/accounts/"+resourceAccount+"/workers/scripts/app-worker" {
			t.Errorf("wrong upload %s %s", r.Method, r.URL)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		parts := map[string][]byte{}
		types := map[string]string{}
		for {
			p, partErr := mr.NextPart()
			if errors.Is(partErr, io.EOF) {
				break
			}
			if partErr != nil {
				t.Fatal(partErr)
			}
			parts[p.FormName()], err = io.ReadAll(p)
			if err != nil {
				t.Fatal(err)
			}
			types[p.FormName()] = p.Header.Get("Content-Type")
		}
		if string(parts["main.mjs"]) != "export default {};" || types["main.mjs"] != "application/javascript+module" {
			t.Fatalf("missing ESM module: %v %v", parts, types)
		}
		var metadata struct {
			Main       string           `json:"main_module"`
			Bindings   []WorkerBinding  `json:"bindings"`
			Migrations WorkerMigrations `json:"migrations"`
			Tags       []string         `json:"tags"`
		}
		if err = json.Unmarshal(parts["metadata"], &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Main != "main.mjs" || len(metadata.Bindings) != 2 || metadata.Bindings[0].Type != "d1" || metadata.Bindings[0].ID != resourceD1ID || metadata.Migrations.NewSQLiteClasses[0] != "Counter" || metadata.Migrations.NewTag != "migration-1" {
			t.Fatalf("incorrect native upload metadata: %s", parts["metadata"])
		}
		fmt.Fprint(w, `{"success":true,"result":{"id":"app-worker","etag":"contenthash","tags":["owner-tag"],"migration_tag":"migration-1"}}`)
	})
	got, err := c.UploadWorker(context.Background(), "token", resourceAccount, "app-worker", WorkerUpload{MainModule: "main.mjs", CompatibilityDate: "2026-10-01", Bindings: []WorkerBinding{{Type: "d1", Name: "DB", ID: resourceD1ID}, {Type: "secret_text", Name: "TOKEN", Text: "secret"}}, Migrations: &WorkerMigrations{NewTag: "migration-1", NewSQLiteClasses: []string{"Counter"}}, Tags: []string{"owner-tag"}, Modules: []WorkerModule{{Name: "main.mjs", Content: []byte("export default {};")}}})
	if err != nil || got.Name != "app-worker" || got.ETag != "contenthash" {
		t.Fatalf("got %#v %v", got, err)
	}
}

func TestWorkerMetadataRevisionAndSubdomainLifecycle(t *testing.T) {
	c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/script-settings"):
			fmt.Fprint(w, `{"success":true,"result":{"tags":["owner-tag"],"bindings":[{"type":"secret_text","name":"TOKEN","text":"hidden"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			fmt.Fprintf(w, `{"success":true,"result":{"deployments":[{"id":"%s","versions":[{"version_id":"%s","percentage":100}]}]}}`, resourceD1ID, resourceD1ID)
		case strings.HasSuffix(r.URL.Path, "/scripts/app-worker/subdomain"):
			if r.Method != "POST" {
				t.Errorf("wrong subdomain method: %s", r.Method)
			}
			fmt.Fprint(w, `{"success":true,"result":{"enabled":true,"previews_enabled":false}}`)
		case strings.HasSuffix(r.URL.Path, "/workers/subdomain"):
			fmt.Fprint(w, `{"success":true,"result":{"subdomain":"example-team"}}`)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	})
	settings, err := c.GetWorkerSettings(context.Background(), "token", resourceAccount, "app-worker")
	if err != nil || len(settings.Tags) != 1 {
		t.Fatalf("settings %#v %v", settings, err)
	}
	data, _ := json.Marshal(settings)
	if strings.Contains(string(data), "hidden") {
		t.Fatalf("secret in settings: %s", data)
	}
	deployment, err := c.GetWorkerDeployment(context.Background(), "token", resourceAccount, "app-worker")
	if err != nil || deployment.Revision() != resourceD1ID {
		t.Fatalf("deployment %#v %v", deployment, err)
	}
	address, err := c.EnableWorkerSubdomain(context.Background(), "token", resourceAccount, "app-worker", true)
	if err != nil || address != "https://app-worker.example-team.workers.dev" {
		t.Fatalf("URL %s %v", address, err)
	}
	if err = c.DeleteWorker(context.Background(), "token", resourceAccount, "app-worker"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerUploadRejectsUnsafePartsBeforeMutation(t *testing.T) {
	c := resourceTestClient(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("invalid Worker upload was sent") })
	for _, upload := range []WorkerUpload{
		{MainModule: "../main.js", CompatibilityDate: "2026-10-01", Modules: []WorkerModule{{Name: "../main.js", Content: []byte("export default {}")}}},
		{MainModule: "main.js", CompatibilityDate: "bad", Modules: []WorkerModule{{Name: "main.js", Content: []byte("export default {}")}}},
		{MainModule: "missing.js", CompatibilityDate: "2026-10-01", Modules: []WorkerModule{{Name: "main.js", Content: []byte("export default {}")}}},
		{MainModule: "main.js", CompatibilityDate: "2026-10-01", Modules: []WorkerModule{{Name: "main.js", Content: []byte("export default {}")}}, Bindings: []WorkerBinding{{Type: "d1", Name: "DB", ID: "../bad"}}},
	} {
		if _, err := c.UploadWorker(context.Background(), "token", resourceAccount, "app-worker", upload); err == nil {
			t.Error("accepted invalid Worker upload")
		}
	}
}
