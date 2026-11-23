package connections

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
)

type registryLoginCheck struct{ RegistryChecker }

func (c registryLoginCheck) Check(_ context.Context, raw []byte, settings map[string]string) (Check, error) {
	if !strings.Contains(string(raw), "temporary-docker") {
		return Check{}, ErrInvalid
	}
	return c.Parse(raw, settings)
}

func TestDigitalOceanRegistryUsesCurrentAccountGrant(t *testing.T) {
	ctx := t.Context()
	r := newSignInRig(t)
	parent, err := r.s.Create(ctx, starter, CreateInput{Cloud: DigitalOcean, Name: "Wakflo", Credential: "dop_v1_team"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer dop_v1_team" {
			t.Error("wrong account authorization")
		}
		switch req.URL.Path {
		case "/v2/registries":
			_, _ = fmt.Fprint(w, `{"registries":[{"name":"wakflo"}]}`)
		case "/v2/registries/wakflo/docker-credentials":
			if req.URL.Query().Get("read_write") != "true" || req.URL.Query().Get("expiry_seconds") != "3600" {
				t.Error("expected expiring read/write credentials")
			}
			calls++
			auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("docker:temporary-docker-%d\n", calls)))
			_ = json.NewEncoder(w).Encode(map[string]any{"auths": map[string]any{digitalocean.RegistryHost: map[string]string{"auth": auth}}})
		default:
			http.NotFound(w, req)
		}
	}))
	defer api.Close()
	r.s.checkers[DigitalOcean] = DigitalOceanChecker{Client: digitalocean.New(api.URL)}
	r.s.checkers[Registry] = registryLoginCheck{}
	names, err := r.s.DigitalOceanRegistries(ctx, starter, parent.ID)
	if err != nil || len(names.Names) != 1 || names.Names[0] != "wakflo" {
		t.Fatalf("list registries: %+v %v", names, err)
	}
	c, err := r.s.Create(ctx, starter, CreateInput{Cloud: Registry, Name: "Wakflo images", RegistryAccountID: parent.ID, Settings: map[string]string{"repository": "wakflo", "endpoint": "https://another.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != MethodDigitalOceanRegistry || c.Details["endpoint"] != "https://registry.digitalocean.com" || c.Status != StatusReady {
		t.Fatal("wrong registry identity or status")
	}
	ref, _ := r.secrets.Reveal(ctx, "org", c.ID)
	if strings.Contains(string(ref), "temporary-docker") || strings.Contains(string(ref), "dop_v1") {
		t.Fatal("stored provider password instead of an account reference")
	}
	if _, err = r.s.Recheck(ctx, starter, c.ID); err != nil {
		t.Fatal(err)
	}
	_, raw, err := r.s.Credential(ctx, "org", c.ID)
	var login struct{ Username, Password string }
	if err != nil || json.Unmarshal(raw, &login) != nil || login.Username != "docker" || login.Password != "temporary-docker-3" {
		t.Fatalf("new registry login: %v", err)
	}
	row := r.store.rows[parent.ID]
	row.Details["team_uuid"] = "another-team"
	r.store.rows[parent.ID] = row
	if _, _, err = r.s.Credential(ctx, "org", c.ID); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed source identity: %v", err)
	}
	delete(r.store.rows, parent.ID)
	if _, _, err = r.s.Credential(ctx, "org", c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed source: %v", err)
	}
	checked, err := r.s.Recheck(ctx, starter, c.ID)
	if err != nil || checked.Status != StatusFailed {
		t.Fatalf("recheck after disconnection: %+v, %v", checked, err)
	}
}

func TestDigitalOceanRegistryUnavailableWrapsUpstreamError(t *testing.T) {
	err := registryAccessError(fmt.Errorf("dial tcp: %w", context.DeadlineExceeded))
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected wrapped ErrUnavailable and DeadlineExceeded, got %v", err)
	}
}
