package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	native "github.com/xraph/loomplane/internal/clouds/redis"
)

func TestCacheSessionRejectsActualImageDrift(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", native.LabelEngine: "redis", owned.LabelImage: "redis@sha256:reviewed"}
	runtime := Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet {
				t.Fatal("image drift reached a mutation")
			}
			info := containerInfo{}
			info.State.Running = true
			info.Config.Image = "redis:unreviewed"
			info.Config.Labels = map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelInstance: iid.String(), native.LabelEngine: "redis"}
			return jsonResponse(info), nil
		})}}, nil
	}}
	client, err := runtime.cacheSession(t.Context(), iid, labels)
	if client != nil {
		client.Close()
	}
	if !errors.Is(err, ErrOwned) {
		t.Fatalf("unreviewed image accepted: %v", err)
	}
}
