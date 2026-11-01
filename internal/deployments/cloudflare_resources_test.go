package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/planning"
)

func TestCloudflareManagedResourceCreateRetriesByNativeIDAndStoresBinding(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service = nil
	job.Action.Subject = planning.ResourceSubject
	job.Action.Resource = &planning.ResourceConfig{Kind: "cloudflare-kv", Settings: map[string]string{}}
	kept := map[string]string{}
	job.Keep = func(_ context.Context, key string, value []byte) error { kept[key] = string(value); return nil }
	var saved []byte
	job.Checkpoint = func(data []byte) error { saved = append([]byte(nil), data...); return nil }
	creates := 0
	name := ""
	deleted := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/namespaces"):
			fmt.Fprint(w, `{"success":true,"result":[]}`)
		case r.Method == "POST":
			creates++
			var body struct {
				Title string `json:"title"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			name = body.Title
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"title":%q}}`, cfResourceID, name)
		case r.Method == "GET":
			fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"title":%q}}`, cfResourceID, name)
		case r.Method == "DELETE":
			deleted = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer s.Close()
	exec := &Cloudflare{Client: cloudflare.New(s.URL, false)}
	out, err := exec.Apply(context.Background(), job)
	if err != nil || out.ExternalID != cfResourceID || out.PhysicalID != cfResourceID {
		t.Fatalf("create %#v %v", out, err)
	}
	if strings.Contains(kept[CloudflareBindingOutput], "token") || !strings.Contains(kept[CloudflareBindingOutput], cfResourceID) || kept[ResourceURL] != "" {
		t.Fatalf("unsafe outputs %v", kept)
	}
	job.Resume = saved
	if _, err = exec.Apply(context.Background(), job); err != nil || creates != 1 {
		t.Fatalf("retry created twice: %d %v", creates, err)
	}
	config, _ := json.Marshal(job.Action.Resource)
	bound := &planning.Binding{Subject: planning.ResourceSubject, ExternalID: out.ExternalID, PhysicalID: out.PhysicalID, Config: config}
	if err = exec.Remove(context.Background(), job, bound); err != nil || !deleted {
		t.Fatalf("remove %v %v", deleted, err)
	}
}

func TestCloudflareManagedResourceRefusesAmbiguousCreateAndReusedBucket(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service = nil
	job.Action.Resource = &planning.ResourceConfig{Kind: "cloudflare-kv"}
	job.Keep = func(context.Context, string, []byte) error { return nil }
	var saved []byte
	job.Checkpoint = func(data []byte) error { saved = append([]byte(nil), data...); return nil }
	creates := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			creates++
			w.WriteHeader(503)
			fmt.Fprint(w, `{"success":false}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"result":[]}`)
	}))
	defer s.Close()
	exec := &Cloudflare{Client: cloudflare.New(s.URL, false)}
	if _, err := exec.Apply(context.Background(), job); err == nil {
		t.Fatal("failed create reported success")
	}
	job.Resume = saved
	if _, err := exec.Apply(context.Background(), job); err == nil || creates != 1 {
		t.Fatalf("ambiguous create repeated: %d %v", creates, err)
	}
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			t.Error("deleted recreated bucket")
		}
		fmt.Fprint(w, `{"success":true,"result":{"name":"assets","creation_date":"2026-11-02T00:00:00Z","jurisdiction":"default"}}`)
	}))
	defer s2.Close()
	config, _ := json.Marshal(planning.ResourceConfig{Kind: "object-storage"})
	err := (&Cloudflare{Client: cloudflare.New(s2.URL, false)}).Remove(context.Background(), job, &planning.Binding{Subject: planning.ResourceSubject, ExternalID: "assets", PhysicalID: "assets@2026-11-01T00:00:00Z", Config: config})
	if err == nil {
		t.Fatal("recreated bucket accepted for deletion")
	}
}

func TestCloudflareManagedResourceResumeRejectsChangedProvisioningSettings(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service = nil
	job.Action.Resource = &planning.ResourceConfig{Kind: "cloudflare-kv", Settings: map[string]string{}}
	job.Keep = func(context.Context, string, []byte) error { return nil }
	var saved []byte
	job.Checkpoint = func(raw []byte) error { saved = append([]byte(nil), raw...); return nil }
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/namespaces") {
			fmt.Fprint(w, `{"success":true,"result":[]}`)
			return
		}
		fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"title":%q}}`, cfResourceID, CloudflareWorkerName(job))
	}))
	defer s.Close()
	exec := &Cloudflare{Client: cloudflare.New(s.URL, false)}
	if _, err := exec.Apply(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	job.Resume = saved
	job.Action.Resource.Settings["jurisdiction"] = "us"
	if _, err := exec.Apply(context.Background(), job); err == nil {
		t.Fatal("changed provisioning settings accepted from an old checkpoint")
	}
}

func TestCloudflareManagedResourcePendingIdentityUsesCheckpointedIncarnation(t *testing.T) {
	job := cfWorkerJob(t)
	job.Action.Service = nil
	job.Action.Subject = planning.ResourceSubject
	job.Action.Resource = &planning.ResourceConfig{Kind: "cloudflare-kv", Settings: map[string]string{}}
	job.Keep = func(context.Context, string, []byte) error { return nil }
	job.Checkpoint = func(raw []byte) error { job.Resume = append([]byte(nil), raw...); return nil }
	deleted := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/namespaces") {
			fmt.Fprint(w, `{"success":true,"result":[]}`)
			return
		}
		if r.Method == "DELETE" {
			deleted = true
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"title":%q}}`, cfResourceID, CloudflareWorkerName(job))
	}))
	defer s.Close()
	exec := &Cloudflare{Client: cloudflare.New(s.URL, false)}
	if _, err := exec.Apply(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.ResolvePendingCloudResource(context.Background(), job)
	if err != nil || identity.ExternalID != cfResourceID || identity.PhysicalID != cfResourceID || !json.Valid(identity.Proof) {
		t.Fatalf("pending identity %#v %v", identity, err)
	}
	if err = exec.RemovePendingCloudResource(context.Background(), job, identity); err != nil || !deleted {
		t.Fatalf("pending remove %v %v", deleted, err)
	}
}
