package discovery

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/repositories"
)

func TestGitLabInstanceHosts_gitlabComFollowsAPIURL(t *testing.T) {
	instance, secrets, err := gitlabInstanceHosts(config.GitLabConfig{
		APIURL: "https://gitlab.test/", ClientID: "com-app", ClientSecret: "com-secret",
		Hosts: []config.GitLabHostConfig{{WebURL: "https://git.example.com", ClientID: "ex-app", ClientSecret: "ex-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(instance) != 2 {
		t.Fatalf("instance hosts = %+v, want gitlab.com and git.example.com", instance)
	}
	com := instance[0]
	if com.Host != repositories.HostGitLab || com.WebURL != "https://gitlab.test" || !com.Instance || com.ClientID != "com-app" {
		t.Fatalf("gitlab.com host = %+v, want host gitlab.com at https://gitlab.test", com)
	}
	if secrets[repositories.HostGitLab] != "com-secret" || secrets["git.example.com"] != "ex-secret" {
		t.Fatal("the instance secrets aren't keyed by host")
	}

	// Lookups go by the host name, never by the address it's reached at.
	reg := &repositories.GitLabRegistry{Instance: instance}
	ctx := context.Background()
	h, ok, err := reg.Host(ctx, "org_a", "gitlab.com")
	if err != nil || !ok || h.WebURL != "https://gitlab.test" {
		t.Fatalf("Host(gitlab.com) = %+v, %v, %v; want the host at https://gitlab.test", h, ok, err)
	}
	if _, byAddress, _ := reg.Host(ctx, "org_a", "gitlab.test"); byAddress {
		t.Fatal("Host found gitlab.com by the host of its address")
	}
	r, ok := reg.Reader(ctx, "gitlab.com")
	if !ok {
		t.Fatal("no reader for gitlab.com")
	}
	if gl, isGitLab := r.(*repositories.GitLab); !isGitLab || gl.BaseURL != "https://gitlab.test" {
		t.Fatalf("gitlab.com reader = %#v, want one reaching https://gitlab.test", r)
	}
}

func TestGitLabInstanceHosts_defaultsAndNoApp(t *testing.T) {
	instance, _, err := gitlabInstanceHosts(config.GitLabConfig{APIURL: "https://gitlab.com"})
	if err != nil || len(instance) != 0 {
		t.Fatalf("without an OAuth application = %+v, %v; want no instance host", instance, err)
	}
	instance, _, err = gitlabInstanceHosts(config.GitLabConfig{APIURL: "https://gitlab.com", ClientID: "a", ClientSecret: "b"})
	if err != nil || len(instance) != 1 || instance[0].WebURL != repositories.DefaultGitLabURL {
		t.Fatalf("default api_url = %+v, %v; want gitlab.com at %s", instance, err, repositories.DefaultGitLabURL)
	}
}
