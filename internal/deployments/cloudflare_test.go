package deployments

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

const cfAccount = "023e105f4ecef8ad9ca31a8372d0c353"
const cfResourceID = "a23e105f4ecef8ad9ca31a8372d0c353"
const cfVersion = "b23e105f-4ece-48ad-9ca3-1a8372d0c353"

func cfSource(t *testing.T, files map[string]string) func(context.Context) (io.ReadCloser, int64, error) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "repository-sha/" + name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	return func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	}
}

func cfWorkerJob(t *testing.T) *Job {
	return &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}, Plan: &planning.Plan{ID: "plan"}, Action: planning.Action{Name: "api", Subject: planning.ServiceSubject, Provider: "cloudflare-workers", Service: &planning.ServiceConfig{Source: "apps/api", Public: true, Worker: &provider.WorkerSettings{Main: "worker.mjs", CompatibilityDate: "2026-11-01"}}}, Connection: &connections.Connection{ID: "connection", Cloud: connections.Cloudflare, Account: cfAccount, Details: map[string]string{"account_id": cfAccount}}, Credential: []byte("token"), SourceID: strings.Repeat("a", 40), CommitSHA: strings.Repeat("a", 40), Source: cfSource(t, map[string]string{"apps/api/worker.mjs": "import {answer} from './answer.js'; export default {fetch(){return new Response(answer)}}", "apps/api/answer.js": "export const answer='ok';", "apps/api/.env": "PRIVATE=hidden"}), Progress: func(string, string) {}, Checkpoint: func([]byte) error { return nil }, Env: map[string]string{"TOKEN": "hidden", "MESSAGE": "hello"}, Secrets: map[string]bool{"TOKEN": true}}
}

func TestCloudflareWorkerDeployResumeObserveAndRemove(t *testing.T) {
	job := cfWorkerJob(t)
	var metadata cloudflare.WorkerUpload
	uploads := 0
	deleted := false
	exists := false
	checkpoints := [][]byte{}
	job.Checkpoint = func(data []byte) error { checkpoints = append(checkpoints, append([]byte(nil), data...)); return nil }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		script := CloudflareWorkerName(job)
		switch {
		case strings.HasSuffix(r.URL.Path, "/script-settings"):
			if !exists {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"success":false}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"tags": metadata.Tags}})
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/scripts/"+script):
			uploads++
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			parts := map[string]string{}
			for {
				p, e := mr.NextPart()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					t.Error(e)
					return
				}
				data, e := io.ReadAll(p)
				if e != nil {
					t.Error(e)
					return
				}
				parts[p.FormName()] = string(data)
			}
			if err = json.Unmarshal([]byte(parts["metadata"]), &metadata); err != nil {
				t.Error(err)
			}
			if parts["worker.mjs"] == "" || parts["answer.js"] == "" || parts[".env"] != "" {
				t.Errorf("wrong uploaded modules: %v", parts)
			}
			if len(metadata.Bindings) != 2 {
				t.Errorf("missing runtime env: %v", metadata.Bindings)
			}
			exists = true
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"etag":"etag","created_on":"2026-11-01T00:00:00Z"}}`, script)
		case strings.HasSuffix(r.URL.Path, "/workers/scripts"):
			fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"created_on":"2026-11-01T00:00:00Z"}]}`, script)
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			fmt.Fprintf(w, `{"success":true,"result":{"deployments":[{"id":%q,"versions":[{"version_id":%q,"percentage":100}]}]}}`, cfVersion, cfVersion)
		case strings.HasSuffix(r.URL.Path, "/workers/subdomain"):
			fmt.Fprint(w, `{"success":true,"result":{"subdomain":"team"}}`)
		case strings.HasSuffix(r.URL.Path, "/subdomain"):
			fmt.Fprint(w, `{"success":true,"result":{"enabled":true}}`)
		case strings.Contains(r.URL.Path, "/durable_objects/namespaces"):
			fmt.Fprint(w, `{"success":true,"result":[]}`)
		case r.Method == "DELETE":
			deleted = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	exec := &Cloudflare{Client: cloudflare.New(server.URL, false)}
	out, err := exec.Apply(context.Background(), job)
	if err != nil || out.ExternalID != CloudflareWorkerName(job) || out.Revision != cfVersion || !strings.HasSuffix(out.URL, ".team.workers.dev") {
		t.Fatalf("got %#v %v", out, err)
	}
	job.Resume = checkpoints[len(checkpoints)-1]
	again, err := exec.Apply(context.Background(), job)
	if err != nil || uploads != 1 || again.Revision != out.Revision {
		t.Fatalf("retry uploaded again: %d %#v %v", uploads, again, err)
	}
	config, _ := json.Marshal(job.Action.Service)
	binding := &planning.Binding{ExternalID: out.ExternalID, PhysicalID: out.PhysicalID, Config: config, Subject: planning.ServiceSubject, ProjectID: "project", EnvironmentID: "env", Name: "api", OrgID: "org"}
	observation, err := exec.Observe(context.Background(), job.Connection, job.Credential, binding)
	if err != nil || !observation.Exists || observation.Revision != cfVersion {
		t.Fatalf("observation %#v %v", observation, err)
	}
	if err = exec.Remove(context.Background(), job, binding); err != nil || !deleted {
		t.Fatalf("remove %v deleted=%v", err, deleted)
	}
}

func TestCloudflareWorkerRejectsUnsafeSourceAndForeignOwnership(t *testing.T) {
	for _, files := range []map[string]string{
		{"apps/api/worker.ts": "export default {}"},
		{"apps/api/../worker.mjs": "export default {}"},
		{"apps/api/worker.mjs": strings.Repeat("x", (2<<20)+1)},
	} {
		job := cfWorkerJob(t)
		job.Source = cfSource(t, files)
		if _, err := cloudflareWorkerModules(context.Background(), job); err == nil {
			t.Fatal("unsafe or missing main accepted")
		}
	}
	job := cfWorkerJob(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("mutated foreign Worker")
		}
		fmt.Fprint(w, `{"success":true,"result":{"tags":["foreign"]}}`)
	}))
	defer server.Close()
	if _, err := (&Cloudflare{Client: cloudflare.New(server.URL, false)}).Apply(context.Background(), job); err == nil {
		t.Fatal("foreign script adopted")
	}
}

func TestCloudflareWorkerBindingsUseNativeResourcesAndNeverMutateImports(t *testing.T) {
	job := cfWorkerJob(t)
	descriptors := []struct{ key, kind, typ, id, extra string }{
		{"DB", "cloudflare-d1", "d1", cfVersion, ""},
		{"CACHE", "cloudflare-kv", "kv_namespace", cfResourceID, ""},
		{"ASSETS", "object-storage", "r2_bucket", "r2/eu/assets", `,"bucket_name":"assets","incarnation":"2026-11-01T00:00:00Z"`},
		{"JOBS", "cloudflare-queue", "queue", cfResourceID, `,"queue_name":"jobs"`},
		{"VECTORS", "cloudflare-vectorize", "vectorize", "embeddings", `,"index_name":"embeddings","incarnation":"2026-11-01T00:00:00Z"`},
		{"POOL", "cloudflare-hyperdrive", "hyperdrive", cfResourceID, ""},
		{"OBJECTS", "cloudflare-durable-object", "durable_object_namespace", cfResourceID, `,"class_name":"Counter","script_name":"owning-worker"`},
	}
	for _, d := range descriptors {
		job.Action.Service.Env = append(job.Action.Service.Env, planning.EnvLink{Key: d.key, Field: CloudflareBindingOutput})
		job.Env[d.key] = fmt.Sprintf(`{"type":%q,"kind":%q,"resource_id":%q,"account_id":%q%s}`, d.typ, d.kind, d.id, cfAccount, d.extra)
	}
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if r.Method != "GET" {
			t.Error("imported resource mutated")
		}
		result := ""
		switch {
		case strings.Contains(r.URL.Path, "/d1/database/"):
			result = fmt.Sprintf(`{"uuid":%q,"name":"db"}`, cfVersion)
		case strings.Contains(r.URL.Path, "/storage/kv/namespaces/"):
			result = fmt.Sprintf(`{"id":%q,"title":"cache"}`, cfResourceID)
		case strings.Contains(r.URL.Path, "/r2/buckets/"):
			if r.Header.Get("cf-r2-jurisdiction") != "eu" {
				t.Error("R2 binding ignored its pinned jurisdiction")
			}
			result = `{"name":"assets","jurisdiction":"eu","creation_date":"2026-11-01T00:00:00Z"}`
		case strings.Contains(r.URL.Path, "/queues/"):
			result = fmt.Sprintf(`{"queue_id":%q,"queue_name":"jobs"}`, cfResourceID)
		case strings.Contains(r.URL.Path, "/vectorize/v2/indexes/"):
			result = `{"name":"embeddings","created_on":"2026-11-01T00:00:00Z"}`
		case strings.Contains(r.URL.Path, "/hyperdrive/configs/"):
			result = fmt.Sprintf(`{"id":%q,"name":"pool","origin":{"password":"must-not-leak"}}`, cfResourceID)
		case strings.Contains(r.URL.Path, "/durable_objects/namespaces"):
			result = fmt.Sprintf(`[{"id":%q,"name":"objects","class":"Counter","script":"owning-worker"}]`, cfResourceID)
		default:
			t.Errorf("unexpected resource read %s", r.URL)
		}
		fmt.Fprintf(w, `{"success":true,"result":%s}`, result)
	}))
	defer server.Close()
	got, err := (&Cloudflare{Client: cloudflare.New(server.URL, false)}).cloudflareWorkerBindings(context.Background(), job, cfAccount)
	if err != nil || len(got) != 9 || reads != 7 {
		t.Fatalf("bindings %#v %v reads %d", got, err, reads)
	}
	for _, binding := range got {
		if binding.Name == "DB" && binding.ID != cfVersion {
			t.Fatalf("D1 binding %#v", binding)
		}
		if binding.Name == "JOBS" && binding.QueueName != "jobs" {
			t.Fatalf("queue binding %#v", binding)
		}
		if binding.Name == "OBJECTS" && (binding.ClassName != "Counter" || binding.ScriptName != "owning-worker") {
			t.Fatalf("DO binding %#v", binding)
		}
		if binding.Name == "TOKEN" && binding.Type != "secret_text" {
			t.Fatal("secret sent as plaintext")
		}
		if binding.Name == "ASSETS" && binding.Jurisdiction != "eu" {
			t.Fatal("R2 jurisdiction lost")
		}
		if binding.Text != "" && binding.Name != "TOKEN" && binding.Name != "MESSAGE" {
			t.Fatalf("native descriptor sent as string: %#v", binding)
		}
	}
}

func TestCloudflareWorkerCreatesLocalDurableObjectClassWithCode(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service.Worker.DurableObjects = []provider.DurableObjectClass{{Name: "COUNTER", ClassName: "Counter"}}
	job.Source = cfSource(t, map[string]string{"apps/api/worker.mjs": "export class Counter {fetch(){return new Response('counter')}};export default {fetch(){return new Response('ok')}}"})
	uploaded := false
	var metadata cloudflare.WorkerUpload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		script := CloudflareWorkerName(job)
		switch {
		case strings.HasSuffix(r.URL.Path, "/script-settings"):
			if !uploaded {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"success":false}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"tags": metadata.Tags}})
		case strings.Contains(r.URL.Path, "/durable_objects/namespaces"):
			if !uploaded {
				fmt.Fprint(w, `{"success":true,"result":[]}`)
				return
			}
			fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"name":"counter-namespace","class":"Counter","script":%q,"use_sqlite":true}]}`, cfResourceID, script)
		case r.Method == "PUT":
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			for {
				part, e := mr.NextPart()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					t.Error(e)
					return
				}
				if part.FormName() == "metadata" {
					if e = json.NewDecoder(part).Decode(&metadata); e != nil {
						t.Error(e)
					}
				}
			}
			if metadata.Migrations == nil || len(metadata.Migrations.NewSQLiteClasses) != 1 || metadata.Migrations.NewSQLiteClasses[0] != "Counter" {
				t.Errorf("missing real DO class migration: %#v", metadata.Migrations)
			}
			found := false
			for _, binding := range metadata.Bindings {
				if binding.Type == "durable_object_namespace" && binding.Name == "COUNTER" && binding.ClassName == "Counter" && binding.ScriptName == "" {
					found = true
				}
			}
			if !found {
				t.Error("missing local DO binding")
			}
			uploaded = true
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"created_on":"2026-11-01T00:00:00Z"}}`, script)
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			fmt.Fprintf(w, `{"success":true,"result":{"deployments":[{"id":%q,"versions":[{"version_id":%q,"percentage":100}]}]}}`, cfVersion, cfVersion)
		case strings.HasSuffix(r.URL.Path, "/workers/subdomain"):
			fmt.Fprint(w, `{"success":true,"result":{"subdomain":"team"}}`)
		case strings.HasSuffix(r.URL.Path, "/subdomain"):
			fmt.Fprint(w, `{"success":true,"result":{"enabled":true}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	out, err := (&Cloudflare{Client: cloudflare.New(server.URL, false)}).Apply(context.Background(), job)
	if err != nil || out.Revision != cfVersion || !uploaded {
		t.Fatalf("DO deployment %#v %v", out, err)
	}
	var owned map[string]json.RawMessage
	if err = json.Unmarshal([]byte(out.OwnedResources), &owned); err != nil {
		t.Fatal(err)
	}
	var receipt CloudflareWorkerOwnedReceipt
	if err = json.Unmarshal(owned["cloudflare-worker"], &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Namespaces) != 1 || receipt.Namespaces[0].ID != cfResourceID || receipt.Namespaces[0].ClassName != "Counter" || receipt.Namespaces[0].CreatedInRelease == "" || receipt.ScriptName != CloudflareWorkerName(job) {
		t.Fatalf("DO ownership receipt %#v", receipt)
	}
}

func TestCloudflareWorkerRefusesRecreatedNativeBucket(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service.Env = []planning.EnvLink{{Key: "ASSETS", Field: CloudflareBindingOutput}}
	job.Env["ASSETS"] = fmt.Sprintf(`{"type":"r2_bucket","kind":"object-storage","resource_id":"r2/default/assets","bucket_name":"assets","account_id":%q,"incarnation":"2026-11-01T00:00:00Z"}`, cfAccount)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"result":{"name":"assets","jurisdiction":"default","creation_date":"2026-11-02T00:00:00Z"}}`)
	}))
	defer s.Close()
	if _, err := (&Cloudflare{Client: cloudflare.New(s.URL, false)}).cloudflareWorkerBindings(context.Background(), job, cfAccount); err == nil {
		t.Fatal("recreated native bucket was attached by name")
	}
}

func TestCloudflareWorkerRemovalPreservesLiveDurableObjectData(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service.Worker.DurableObjects = []provider.DurableObjectClass{{Name: "COUNTER", ClassName: "Counter"}}
	script := CloudflareWorkerName(job)
	config, _ := json.Marshal(job.Action.Service)
	b := &planning.Binding{Subject: planning.ServiceSubject, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: "api", ExternalID: script, PhysicalID: script + "@2026-11-01T00:00:00Z", Config: config}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			t.Error("deleted live Durable Object data")
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/script-settings"):
			fmt.Fprintf(w, `{"success":true,"result":{"tags":[%q]}}`, cloudflareOwner("org", "project", "env", "api"))
		case strings.HasSuffix(r.URL.Path, "/workers/scripts"):
			fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"created_on":"2026-11-01T00:00:00Z","migration_tag":"first"}]}`, script)
		case strings.Contains(r.URL.Path, "/durable_objects/namespaces"):
			fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"class":"Counter","script":%q}]}`, cfResourceID, script)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer s.Close()
	if err := (&Cloudflare{Client: cloudflare.New(s.URL, false)}).Remove(context.Background(), job, b); err == nil {
		t.Fatal("live DO Worker removal bypassed class lifecycle review")
	}
}

func TestCloudflareWorkerReceiptExcludesImportedDurableObjects(t *testing.T) {
	state := cloudflareWorkerState{Account: cfAccount, Script: "owned-worker", Owner: "owner", Release: "release", CreatedClasses: []string{"Counter"}}
	namespaces := []cloudflare.Resource{{ID: cfResourceID, Kind: cloudflare.ResourceDurableObject, Metadata: map[string]string{"class": "Counter", "script": "owned-worker"}}, {ID: cfAccount, Kind: cloudflare.ResourceDurableObject, Metadata: map[string]string{"class": "Other", "script": "shared-worker"}}}
	if err := cloudflareConfirmNamespaces(&state, namespaces); err != nil {
		t.Fatal(err)
	}
	if len(state.Namespaces) != 1 || state.Namespaces[0].ID != cfResourceID {
		t.Fatalf("imported namespace claimed: %#v", state.Namespaces)
	}
}

func TestCloudflareWorkerRejectsChangedOwnedNamespaceBeforeUploadingCode(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service.Worker.DurableObjects = []provider.DurableObjectClass{{Name: "COUNTER", ClassName: "Counter"}}
	script := CloudflareWorkerName(job)
	owner := cloudflareOwner("org", "project", "env", "api")
	born := "2026-11-01T00:00:00Z"
	raw, _ := json.Marshal(CloudflareWorkerOwnedReceipt{AccountID: cfAccount, ScriptName: script, ScriptCreatedAt: born, OwnerTag: owner, ConfigDigest: cloudflareConfigurationDigest(job.Action.Name, job.Action.Service), Namespaces: []CloudflareWorkerOwnedNamespace{{ID: cfResourceID, ClassName: "Counter", CreatedInRelease: "prior-release"}}})
	previousConfig, _ := json.Marshal(job.Action.Service)
	job.Binding = &planning.Binding{Name: job.Action.Name, Config: previousConfig, ExternalID: script, PhysicalID: script + "@" + born, OwnedResources: map[string]json.RawMessage{"cloudflare-worker": raw}}
	uploads := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/script-settings"):
			fmt.Fprintf(w, `{"success":true,"result":{"tags":[%q,"prior-release"]}}`, owner)
		case strings.HasSuffix(r.URL.Path, "/workers/scripts"):
			fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"created_on":%q}]}`, script, born)
		case strings.Contains(r.URL.Path, "/durable_objects/namespaces"):
			fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"class":"Counter","script":%q}]}`, cfAccount, script)
		case r.Method == "PUT":
			uploads++
			w.WriteHeader(400)
			fmt.Fprint(w, `{"success":false}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer s.Close()
	if _, err := (&Cloudflare{Client: cloudflare.New(s.URL, false)}).Apply(context.Background(), job); err == nil || uploads != 0 {
		t.Fatalf("code uploaded over changed namespace: uploads=%d err=%v", uploads, err)
	}
}
