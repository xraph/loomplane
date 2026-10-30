//go:build acceptance

package firecracker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in only against a disposable host. LIVE_CONFIG contains Endpoint, HostID,
// Org, Connection and Credential (the mTLS Credential object). LIVE_IMAGE must
// be digest-pinned and provide /bin/sh, mkdir, printf and busybox httpd.
// Optional LIVE_BUILD_IMAGE is a registry image with a hexadecimal tag;
// LIVE_REGISTRY_CONFIG contains scoped Docker credentials for that repository.
// Optional LIVE_RESTART_HOOK is an absolute executable that restarts this agent.
func TestFirecrackerLive(t *testing.T) {
	if os.Getenv("LOOMPLANE_FIRECRACKER_LIVE") != "1" {
		t.Skip("requires an explicitly authorized disposable Linux/KVM host")
	}
	var config struct {
		Endpoint, HostID, Org, Connection string
		Credential                        Credential
	}
	raw, err := os.ReadFile(os.Getenv("LOOMPLANE_FIRECRACKER_LIVE_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(config.Credential)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(credential, map[string]string{"endpoint": config.Endpoint, "host_id": config.HostID})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	caps, err := client.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("live-%d", time.Now().UnixNano())
	q := Request{Scope: Scope{Org: config.Org, Connection: config.Connection, Project: "acceptance", Environment: suffix, Resource: "app"}, RuntimeIdentity: caps.RuntimeIdentity, Spec: Spec{Kind: "app", Service: "api", Image: os.Getenv("LOOMPLANE_FIRECRACKER_LIVE_IMAGE"), VCPUs: 1, MemoryMiB: 512, DiskMiB: 1024, Port: 8080, HealthPath: "/", Command: []string{"/bin/sh"}, Args: []string{"-c", "mkdir -p /tmp/www; printf first > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www"}}}
	generation := int64(0)
	authorize := func(request *Request) {
		generation++
		request.Authority = Authority{Generation: generation, Operation: fmt.Sprintf("acceptance-%d", generation), Expires: time.Now().Add(4 * time.Minute)}
	}
	authorize(&q)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		authorize(&q)
		if _, e := client.Mutate(cleanup, q, "remove"); e != nil {
			t.Errorf("application cleanup: %v", e)
		}
	})
	first, err := client.Apply(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.Apply(t.Context(), q)
	if err != nil || again.Current == nil || first.Current == nil || again.Current.ID != first.Current.ID {
		t.Fatal("idempotency failed", err)
	}
	read := func(want string) {
		t.Helper()
		r, e := client.Inspect(t.Context(), q.Scope)
		if e != nil {
			t.Fatal(e)
		}
		request := q
		request.Authority.Generation = r.Generation
		conn, e := client.DialService(t.Context(), request)
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
		defer transport.CloseIdleConnections()
		httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
		req, e := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://guest/", http.NoBody)
		if e != nil {
			t.Fatal(e)
		}
		response, e := httpClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, 4096))
		if e != nil || string(body) != want {
			t.Fatalf("response=%q want=%q error=%v", body, want, e)
		}
	}
	read("first")
	q.Spec.Args[1] = strings.Replace(q.Spec.Args[1], "printf first", "printf second", 1)
	authorize(&q)
	if _, err = client.Apply(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	read("second")
	q.Spec.Args[1] = strings.Replace(q.Spec.Args[1], "printf second", "printf first", 1)
	authorize(&q)
	if _, err = client.Apply(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	read("first")
	if hook := os.Getenv("LOOMPLANE_FIRECRACKER_LIVE_RESTART_HOOK"); hook != "" {
		if !filepath.IsAbs(hook) {
			t.Fatal("restart hook must be absolute")
		}
		if err = exec.CommandContext(t.Context(), hook).Run(); err != nil {
			t.Fatal(err)
		}
		read("first")
	} else {
		t.Log("agent restart hook not supplied; live restart not exercised")
	}
	authorize(&q)
	stopped, err := client.Mutate(t.Context(), q, "stop")
	if err != nil {
		t.Fatal(err)
	}
	authorize(&q)
	started, err := client.Apply(t.Context(), q)
	if err != nil || started.Current == nil || stopped.Current == nil || started.Current.ID != stopped.Current.ID {
		t.Fatal("restart lost allocation", err)
	}
	read("first")
	if target := os.Getenv("LOOMPLANE_FIRECRACKER_LIVE_BUILD_IMAGE"); target != "" {
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		dockerfile := []byte("FROM " + q.Spec.Image + "\nRUN echo isolated-build\n")
		if err = writer.WriteHeader(&tar.Header{Name: ".loomplane-build/Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(dockerfile); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(archive.Bytes())
		build := q
		build.Scope.Resource = "build"
		build.Spec = Spec{Kind: "build", VCPUs: 2, MemoryMiB: 2048, DiskMiB: 8192, Dockerfile: ".loomplane-build/Dockerfile", BuildImage: target, SourceDigest: fmt.Sprintf("%x", digest)}
		build.Values.RegistryConfig, err = os.ReadFile(os.Getenv("LOOMPLANE_FIRECRACKER_LIVE_REGISTRY_CONFIG"))
		if err != nil {
			t.Fatal(err)
		}
		authorize(&build)
		t.Cleanup(func() {
			authorize(&build)
			if _, e := client.Mutate(context.Background(), build, "remove"); e != nil {
				t.Errorf("build cleanup: %v", e)
			}
		})
		if _, err = client.Apply(t.Context(), build); err != nil {
			t.Fatal(err)
		}
		if _, err = client.Guest(t.Context(), build, "source", bytes.NewReader(archive.Bytes())); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Minute)
		for {
			build.Authority.Expires = time.Now().Add(4 * time.Minute)
			status, e := client.Guest(t.Context(), build, "status", nil)
			if e != nil {
				t.Fatal(e)
			}
			if status.State == "succeeded" {
				if !strings.HasPrefix(status.Digest, "sha256:") {
					t.Fatal("missing digest")
				}
				break
			}
			if status.State == "failed" || time.Now().After(deadline) {
				t.Fatal("build failed or timed out")
			}
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-time.After(time.Second):
			}
		}
	} else {
		t.Log("registry build inputs not supplied; live image push not exercised")
	}
}
