package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChooseEnvironmentConnectionScopesAndPropagatesRefusal(t *testing.T) {
	refused := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/v1/projects/project%2Fscope/environments/preview%20two/connections/gcp" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unscoped connection choice: %s %s", r.Method, r.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["connection_id"] != "selected" {
			t.Errorf("incorrect connection body: %+v %v", body, err)
		}
		if refused {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"selection refused"}`))
			return
		}
		_, _ = w.Write([]byte(`{"environment":"preview two","connections":{"gcp":"selected"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "test-key")
	if err := c.ChooseEnvironmentConnection(t.Context(), "project/scope", "preview two", "gcp", "selected"); err != nil {
		t.Fatal(err)
	}
	refused = true
	err := c.ChooseEnvironmentConnection(t.Context(), "project/scope", "preview two", "gcp", "selected")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Message != "selection refused" {
		t.Fatalf("connection refusal was lost: %v", err)
	}
}

func TestManagedPlanKeepsSelectedAccountForReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"plan","catalog":true,"connections":{"aws":"selected"},"actions":[{"id":"resource:database","provider":"rds"}]}`))
	}))
	defer srv.Close()
	plan, err := New(srv.URL, "test-key").Plan(t.Context(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Catalog || plan.Connections["aws"] != "selected" {
		t.Fatal("the CLI plan discarded the selected deployment account")
	}
}
