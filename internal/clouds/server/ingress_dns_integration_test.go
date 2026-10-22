//go:build integration

package server

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

const testIngressTag = "loomplane-ingress:test"

var testIngressBuild struct {
	once sync.Once
	out  []byte
	err  error
}

// testIngressImage builds deployments/ingress once per run and, given a
// fixture host, loads it into that host's Docker. Tests pin the tag in place
// of the published digest.
func testIngressImage(t *testing.T, host string) string {
	t.Helper()
	testIngressBuild.once.Do(func() {
		testIngressBuild.out, testIngressBuild.err = exec.CommandContext(context.Background(), "docker", "build", "-q", "-f", "../../../deployments/ingress/Dockerfile", "-t", testIngressTag, "../../../deployments/ingress").CombinedOutput()
	})
	if testIngressBuild.err != nil {
		t.Fatalf("build the ingress image: %v\n%s", testIngressBuild.err, testIngressBuild.out)
	}
	if host != "" {
		if out, err := exec.CommandContext(t.Context(), "sh", "-c", "docker save "+testIngressTag+" | docker exec -i "+host+" docker load").CombinedOutput(); err != nil {
			t.Fatalf("load the ingress image: %v\n%s", err, out)
		}
	}
	return testIngressTag
}

func proxyLogs(t *testing.T, host, server string) string {
	t.Helper()
	out, _ := exec.CommandContext(context.Background(), "docker", "exec", host, "docker", "logs", IngressName("org", server)).CombinedOutput()
	return string(out)
}

func TestIngressConfigNeverEmbedsDNSToken(t *testing.T) {
	c, host := ingressFixture(t)
	pinIngressImageForTest(t, testIngressImage(t, host))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Caddy diagnostics: %s", proxyLogs(t, host, "server"))
		}
	})
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/server", Generation: 1})
	snapshot, err := EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "cf-secret-token-0123456789abcdefghijklmn"
	if err = WriteIngressDNSToken(ctx, c, "org", "server", "zone_1", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	if mode := ingressDocker(t, "exec", host, "docker", "exec", IngressName("org", "server"), "stat", "-c", "%a %u", "/config/dns/zone_1.token", "/config/dns"); mode != "400 0\n700 0" {
		t.Fatalf("token file mode: %q", mode)
	}
	// The app runs the stock image, which only the proxy pull would fetch.
	ingressDocker(t, "exec", host, "docker", "pull", "-q", LegacyIngressImage)
	backend := ingressApp(ctx, t, c, host, "server", "docs")
	// The directory is unreachable, so Caddy provisions the DNS module with
	// the token and never orders a certificate or calls Cloudflare.
	config := IngressConfig{OrgID: "org", ServerID: "server", Version: "wildcard", ACMEDir: "https://127.0.0.1:1/directory", Routes: []IngressRoute{
		{ID: "route_w", Hostname: "*.example.test", DNSChallenge: &IngressDNSChallenge{ZoneID: "zone_1", Zone: "example.test"}, Backend: backend},
	}}
	applied, err := ApplyIngress(ctx, c, snapshot, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = ProbeIngressBackend(ctx, c, config, applied, "route_w"); err != nil {
		t.Fatal("wildcard route through its probe hostname", err)
	}
	raw, err := config.JSON()
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	// IngressState persists the config, its snapshots and nothing else.
	persisted, err := json.Marshal(map[string]any{"config": config, "current": read, "previous": snapshot})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"config": string(raw), "readback": string(read.Config), "persisted state": string(persisted), "Caddy log": proxyLogs(t, host, "server")} {
		if strings.Contains(v, secret) {
			t.Fatalf("the %s holds the token", name)
		}
	}
	if !strings.Contains(string(read.Config), "{file./config/dns/zone_1.token}") {
		t.Fatal("readback lost the placeholder")
	}
	if err = RemoveIngressDNSToken(ctx, c, "org", "server", "zone_1"); err != nil {
		t.Fatal(err)
	}
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "exec", IngressName("org", "server"), "test", "-e", "/config/dns/zone_1.token").Run() == nil {
		t.Fatal("token file survived removal")
	}
	if err = RemoveIngressDNSToken(ctx, c, "org", "server", "zone_1"); err != nil {
		t.Fatal("removing a missing token", err)
	}
}

func TestIngressUpgradeKeepsConfigAndRoutes(t *testing.T) {
	c, host := ingressFixture(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Caddy diagnostics: %s", proxyLogs(t, host, "server"))
		}
	})
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/server", Generation: 1})
	pinIngressImageForTest(t, "")
	snapshot, err := EnsureIngress(ctx, c, "org", "server")
	if err != nil {
		t.Fatal(err)
	}
	proxy := IngressName("org", "server")
	if image := ingressDocker(t, "exec", host, "docker", "inspect", "--format", "{{.Config.Image}}", proxy); image != LegacyIngressImage {
		t.Fatal("an unpublished image made a new proxy", image)
	}
	backend := ingressApp(ctx, t, c, host, "server", "shop")
	config := IngressConfig{OrgID: "org", ServerID: "server", Version: "v1", InternalCA: true, Routes: []IngressRoute{{ID: "route_a", Hostname: "a.ingress.test", Backend: backend}}}
	current, err := ApplyIngress(ctx, c, snapshot, config)
	if err != nil {
		t.Fatal(err)
	}
	if upgrade, needErr := IngressNeedsUpgrade(ctx, c, "org", "server"); needErr != nil || !upgrade {
		t.Fatal("stock proxy without a published image", upgrade, needErr)
	}

	// A new image that can't start leaves the old proxy serving.
	ingressDocker(t, "exec", host, "docker", "run", "--name", "broken", LegacyIngressImage, "rm", "/usr/bin/caddy")
	ingressDocker(t, "exec", host, "docker", "commit", "broken", "loomplane.test/broken:1")
	pinIngressImageForTest(t, "loomplane.test/broken:1")
	if _, err = UpgradeIngress(ctx, c, "org", "server", current); err == nil {
		t.Fatal("upgraded to an image without Caddy")
	}
	after, err := ReadIngress(ctx, c, "org", "server")
	if err != nil || after.ContainerID != current.ContainerID || !sameIngressJSON(after.Config, current.Config) {
		t.Fatal("the old proxy didn't come back", err)
	}
	if err = ProbeIngressBackend(ctx, c, config, after, "route_a"); err != nil {
		t.Fatal("route after a failed upgrade", err)
	}
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "inspect", previousIngressName("org", "server")).Run() == nil {
		t.Fatal("a failed upgrade left the old proxy renamed")
	}

	image := testIngressImage(t, host)
	pinIngressImageForTest(t, image)
	if upgrade, needErr := IngressNeedsUpgrade(ctx, c, "org", "server"); needErr != nil || !upgrade {
		t.Fatal("stock proxy with a published image", upgrade, needErr)
	}
	upgraded, err := UpgradeIngress(ctx, c, "org", "server", current)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.ContainerID == current.ContainerID || !sameIngressJSON(upgraded.Config, current.Config) {
		t.Fatal("upgrade didn't resume the same config on a new proxy")
	}
	if got := ingressDocker(t, "exec", host, "docker", "inspect", "--format", "{{.Config.Image}}", proxy); got != image {
		t.Fatal("upgraded proxy runs", got)
	}
	if upgrade, needErr := IngressNeedsUpgrade(ctx, c, "org", "server"); needErr != nil || upgrade {
		t.Fatal("upgraded proxy still needs an upgrade", upgrade, needErr)
	}
	if err = ProbeIngressBackend(ctx, c, config, upgraded, "route_a"); err != nil {
		t.Fatal("route after the upgrade", err)
	}
	if _, err = ConnectIngressBackend(ctx, c, "org", "server", backend); err != nil {
		t.Fatal("the route network holds more than the backend and the new proxy", err)
	}
	if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "inspect", previousIngressName("org", "server")).Run() == nil {
		t.Fatal("the old proxy is left behind")
	}
	// Running it again finds the work done.
	if again, againErr := UpgradeIngress(ctx, c, "org", "server", upgraded); againErr != nil || again.ContainerID != upgraded.ContainerID {
		t.Fatal("a repeated upgrade", againErr)
	}
}
