package cloudinfra

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
)

// A capacity block answers 422 with its sentence, so the dashboard shows why
// instead of asking the person to reload. A moved revision and a busy
// operation stay 409.
func TestResultCapacityStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"hard limit", &infra.CapacityAdmissionError{Block: infra.CapacityBlock{Reason: "hard_limit", Resource: "servers", Counted: 2, Limit: 2, Requested: 1}}, http.StatusUnprocessableEntity, "Servers is at this Cloud's limit: 2 of 2 counted, 1 more needed."},
		{"wrapped rollout", fmt.Errorf("approve: %w", &infra.CapacityAdmissionError{Block: infra.CapacityBlock{Reason: "review_required", Resource: "rollout"}}), http.StatusUnprocessableEntity, infra.RolloutOffText},
		{"revision moved", infra.ErrConflict, http.StatusConflict, infra.ErrConflict.Error()},
		{"busy", infra.ErrBusy, http.StatusConflict, infra.ErrBusy.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := forge.NewRouter()
			if err := r.GET("/x", func(c forge.Context) error { return result(c, http.StatusOK, nil, tc.err) }); err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			r.ServeHTTP(out, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))
			if out.Code != tc.code || !strings.Contains(out.Body.String(), tc.body) {
				t.Fatalf("got %d %s", out.Code, out.Body.String())
			}
		})
	}
}
