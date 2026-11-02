package planning

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestCatalogAdminRefusalsKeepTheirMessage(t *testing.T) {
	svc := catalogproxy.NewService(catalogproxy.Deps{Authz: allowAll{}, Bindings: func(context.Context, string, string) ([]*planning.Binding, error) {
		return []*planning.Binding{{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "db", Config: []byte(`{"provider":"server","kind":"postgres"}`)}}, nil
	}})
	p := security.Principal{OrgID: "org", UserID: "usr"}
	for resource, want := range map[string]struct {
		status int
		msg    string
	}{
		"db":   {http.StatusUnprocessableEntity, "postgres has no admin to open"},
		"nope": {http.StatusNotFound, "catalog admin not found: this environment has no catalog resource nope"},
	} {
		_, err := svc.Open(context.Background(), p, deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: resource})
		var httpErr interface {
			error
			StatusCode() int
		}
		if !errors.As(fail(err), &httpErr) || httpErr.StatusCode() != want.status || httpErr.Error() != want.msg {
			t.Fatalf("%s: %v -> %+v", resource, err, httpErr)
		}
	}
}
