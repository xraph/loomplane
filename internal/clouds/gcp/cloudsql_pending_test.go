package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAwaitSQLMutationOnlyObservesOriginalOperation(t *testing.T) {
	for _, scenario := range []string{"complete", "updated-user", "wrong-project", "wrong-target", "wrong-kind", "wrong-name", "missing-kind", "missing", "empty", "guard"} {
		t.Run(scenario, func(t *testing.T) {
			reads, writes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes++
					t.Error("reissued creation")
				}
				reads++
				if r.URL.Path != "/v1/projects/project-test/operations/original" {
					t.Error("wrong operation path", r.URL.Path)
				}
				if scenario == "missing" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				op := map[string]string{"kind": "sql#operation", "name": "original", "status": "DONE", "targetProject": "project-test", "targetId": "database", "operationType": "CREATE_DATABASE"}
				switch scenario {
				case "updated-user":
					op["operationType"] = "UPDATE_USER"
				case "wrong-project":
					op["targetProject"] = "foreign"
				case "wrong-target":
					op["targetId"] = "foreign"
				case "wrong-kind":
					op["operationType"] = "DELETE"
				case "wrong-name":
					op["name"] = "foreign"
				case "missing-kind":
					delete(op, "operationType")
				}
				_ = json.NewEncoder(w).Encode(op)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
			operation := "original"
			if scenario == "empty" {
				operation = ""
			}
			kind := "CREATE_DATABASE"
			if scenario == "updated-user" {
				kind = "CREATE_USER"
			}
			err := s.AwaitSQLMutation(t.Context(), "database", operation, kind, func(context.Context) error {
				if scenario == "guard" {
					return errors.New("lease lost")
				}
				return nil
			})
			if (err == nil) != (scenario == "complete" || scenario == "updated-user") || writes != 0 {
				t.Fatal("unsafe operation acceptance", err, writes)
			}
			if (scenario == "guard" || scenario == "empty") && reads != 0 {
				t.Fatal("read after refusal", reads)
			}
		})
	}
}
