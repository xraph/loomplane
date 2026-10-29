//go:build integration

package kubernetes

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// This opt-in test uses an existing disposable registry. It creates a build
// Job and leaves one smoke-test image for the registry's normal retention.
func TestKataBuildIntegration(t *testing.T) {
	path := os.Getenv("LOOMPLANE_KATA_TEST_CONFIG")
	if path == "" {
		t.Skip("set LOOMPLANE_KATA_TEST_CONFIG for a disposable Linux/Kata build target")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Kubeconfig string            `json:"kubeconfig"`
		Org        string            `json:"org"`
		Connection string            `json:"connection"`
		Settings   map[string]string `json:"settings"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if input.Settings["build_workers"] != "shared" || input.Settings["build_runtime_class"] == "" {
		t.Fatal("the test requires shared workers and an explicit Kata RuntimeClass")
	}
	raw, err = os.ReadFile(input.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(raw, input.Settings)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := c.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(checked.Missing) != 0 || checked.Details["managed_builds"] != "ready" {
		t.Fatalf("build prerequisites: %v", checked.Missing)
	}
	c.Config.Details = checked.Details
	secret, err := c.registrySecret(t.Context(), input.Org, input.Connection, false)
	if err != nil {
		t.Fatal(err)
	}
	name, _, err := registryIdentity(input.Org, input.Connection, checked.Details["namespace_uid"])
	if err != nil {
		t.Fatal(err)
	}
	registry, err := registryCredentials(secret, name, checked.Details["registry_hostname"])
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM " + buildkitImage + "\nRUN test $(id -u) -ne 0 && echo kata-build-smoke\n"
	var source bytes.Buffer
	archive := tar.NewWriter(&source)
	if err = archive.WriteHeader(&tar.Header{Name: ".loomplane-build/Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
		t.Fatal(err)
	}
	if _, err = archive.Write([]byte(dockerfile)); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("%x", rand.Text())[:24]
	var output bytes.Buffer
	builder := &ClusterBuilder{Client: c}
	digest, err := builder.Build(t.Context(), ClusterImageBuild{Org: input.Org, Connection: input.Connection, Operation: "kata-smoke-" + tag, Dockerfile: ".loomplane-build/Dockerfile", Image: registry.Host + "/loomplane/kata-smoke:" + tag, Source: &source}, registry, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !buildDigest.MatchString(digest) || !strings.Contains(output.String(), "kata-build-smoke") {
		t.Fatal("build did not execute its command and publish a digest")
	}
}
