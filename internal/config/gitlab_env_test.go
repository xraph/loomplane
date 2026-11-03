package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadRepoConfig loads the repository's own config.yaml, so a test sees
// exactly how its environment variables are wired.
func loadRepoConfig(t *testing.T) (Config, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
	return Load(dir)
}

func TestLoad_gitlabDefaultsFromConfigYAML(t *testing.T) {
	c, err := loadRepoConfig(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := c.GitLab
	if g.APIURL != "https://gitlab.com" || g.ClientID != "" || g.ClientSecret != "" || g.WebhookURL != "" || !g.AllowOrganizationHosts || len(g.Hosts) != 0 {
		t.Fatalf("GitLab defaults %+v", g)
	}
}

func TestLoad_gitlabFromEnvironment(t *testing.T) {
	t.Setenv("LOOMPLANE_GITLAB_API_URL", "https://gitlab.test")
	t.Setenv("LOOMPLANE_GITLAB_CLIENT_ID", "app-id")
	t.Setenv("LOOMPLANE_GITLAB_CLIENT_SECRET", "app-secret")
	t.Setenv("LOOMPLANE_GITLAB_ALLOW_ORGANIZATION_HOSTS", "false")
	t.Setenv("LOOMPLANE_GITLAB_WEBHOOK_URL", "https://hooks.example.com")
	c, err := loadRepoConfig(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := c.GitLab
	if g.APIURL != "https://gitlab.test" || g.ClientID != "app-id" || g.ClientSecret != "app-secret" ||
		g.AllowOrganizationHosts || g.WebhookURL != "https://hooks.example.com" {
		t.Fatalf("GitLab %+v", g)
	}
}

func TestLoad_gitlabClientNeedsBothFromEnvironment(t *testing.T) {
	t.Setenv("LOOMPLANE_GITLAB_CLIENT_ID", "app-id")
	_, err := loadRepoConfig(t)
	if err == nil || !strings.Contains(err.Error(), "gitlab.client_secret is required") {
		t.Fatalf("Load error %v, want gitlab.client_secret is required", err)
	}
}
