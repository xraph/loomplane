package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

const serverTarget = "conn_01m4vewksnea2bwdxwdy5aekhn"
const clusterTarget = "conn_01m4va49zwe9pvk24sm9h8v0ty"

func TestInfrastructureManifest_targetsAndBuilds(t *testing.T) {
	doc := `version: 1
services:
  api:
    source: go:cmd/api
    provider: server
    target: ` + serverTarget + `
    health_path: /_/health
    endpoint: {host_port: 8081}
    build: {builder: ` + serverTarget + `, registry: ` + clusterTarget + `, context: .}
    environments:
      preview:
        provider: kubernetes
        target: ` + clusterTarget + `
        endpoint: {hostname: api.example.com, ingress_class: nginx, tls_secret: app-tls}
        build: {image: registry.example.com/acme/api@sha256:` + strings.Repeat("a", 64) + `}
      staging:
        provider: vercel
`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ env, target, endpoint string }{
		{"production", serverTarget, `"host_port":8081`},
		{"preview", clusterTarget, `"hostname":"api.example.com"`},
		{"staging", "", ""},
	} {
		set := m.Services["api"].For(tc.env, m.Defaults)
		data, encodeErr := json.Marshal(set)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		var fields map[string]any
		if err = json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if tc.target == "" {
			if fields["target"] != nil || fields["endpoint"] != nil {
				t.Fatalf("%s carried another provider's placement: %s", tc.env, data)
			}
		} else if fields["target"] != tc.target || !strings.Contains(string(data), tc.endpoint) {
			t.Fatalf("%s placement: %s", tc.env, data)
		}
		if fields["health_path"] != "/_/health" {
			t.Fatalf("lost the app's health path: %s", data)
		}
	}
	preview, err := json.Marshal(m.Services["api"].For("preview", m.Defaults))
	if err != nil || !strings.Contains(string(preview), `"image":"registry.example.com/acme/api@sha256:`) {
		t.Fatalf("environment build: %s %v", preview, err)
	}
}

func TestInfrastructureManifest_rejectsInvalidPlacement(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{"target: another_org", "services.api.target"},
		{"endpoint: {host_port: 65536}", "services.api.endpoint.host_port"},
		{"endpoint: {hostname: 'a b.example'}", "services.api.endpoint.hostname"},
		{"endpoint: {tls_secret: '../key'}", "services.api.endpoint.tls_secret"},
		{"health_path: '//outside.example/health'", "services.api.health_path"},
		{"health_path: '/health?token=secret'", "services.api.health_path"},
		{"build: {builder: invalid}", "services.api.build.builder"},
		{"build: {registry: invalid}", "services.api.build.registry"},
		{"build: {image: 'acme/app:latest'}", "services.api.build.image"},
		{"build: {image: 'acme/app@sha256:" + strings.Repeat("g", 64) + "'}", "services.api.build.image"},
	} {
		t.Run(tc.want+tc.field, func(t *testing.T) {
			_, err := Parse([]byte("version: 1\nservices:\n  api:\n    source: cmd/api\n    " + tc.field + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation: %v, want path %s", err, tc.want)
			}
		})
	}
}

func TestInfrastructurePlacement_editsTargetAndEnvironmentBuild(t *testing.T) {
	var edit Placement
	if err := json.Unmarshal([]byte(`{"section":"services","name":"api","environment":"preview","provider":"server","target":"`+serverTarget+`","endpoint":{"host_port":8082},"build":{"builder":"`+serverTarget+`","context":"."}}`), &edit); err != nil {
		t.Fatal(err)
	}
	out, err := WithPlacement([]byte(commented), edit)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	set, err := json.Marshal(m.Services["api"].For("preview", m.Defaults))
	if err != nil || !strings.Contains(string(set), `"target":"`+serverTarget+`"`) || !strings.Contains(string(set), `"host_port":8082`) || !strings.Contains(string(set), `"context":"."`) {
		t.Fatalf("placement fields missing: %s %v", set, err)
	}
	if !strings.Contains(string(out), "# the Go module") || m.Services["api"].Provider != "cloud-run" {
		t.Fatalf("placement damaged other settings: %s", out)
	}
}
