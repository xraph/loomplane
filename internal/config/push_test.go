package config

import (
	"strings"
	"testing"
)

func TestPushDefaults(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Push.Enabled {
		t.Fatal("push is off with nothing set")
	}
	if c.Push.GatewayURL != "https://push.loomplane.com" {
		t.Fatalf("GatewayURL = %q", c.Push.GatewayURL)
	}
}

func TestPushConfigLoadsAndRejectsUnsafeURLs(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		enabled string
		url     string
		want    string
	}{
		{"http in production", EnvProduction, "true", "http://gw.example.com", "https"},
		{"user info", EnvDevelopment, "true", "https://u:p@gw.example.com", "user"},
		{"relative", EnvDevelopment, "true", "gw.example.com", "absolute"},
		{"query", EnvDevelopment, "true", "https://gw.example.com/?a=b", "query"},
		{"fragment", EnvDevelopment, "true", "https://gw.example.com/#x", "fragment"},
		{"http localhost in development", EnvDevelopment, "true", "http://localhost:9000", ""},
		{"http 127.0.0.1 in development", EnvDevelopment, "true", "http://127.0.0.1:9000", ""},
		{"http ::1 in development", EnvDevelopment, "true", "http://[::1]:9000", ""},
		{"http other host in development", EnvDevelopment, "true", "http://gw.internal", "https"},
		{"http localhost in production", EnvProduction, "true", "http://localhost", "https"},
		{"https in production", EnvProduction, "true", "https://gw.example.com", ""},
		{"off with no url", EnvDevelopment, "false", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PushConfig{Enabled: tt.enabled == "true", GatewayURL: tt.url}
			err := p.Validate(tt.env)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestPushLoadsFromSource(t *testing.T) {
	c, err := FromSource(mapGetter{
		"database.url": testDSN, "push.enabled": "false", "push.gateway_url": "https://gw.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Push.Enabled || c.Push.GatewayURL != "https://gw.example.com" {
		t.Fatalf("Push = %+v", c.Push)
	}
	if _, err := FromSource(mapGetter{"database.url": testDSN, "push.gateway_url": "ftp://x"}); err == nil {
		t.Fatal("an ftp gateway URL was accepted")
	}
}
