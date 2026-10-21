package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSQLIncarnationUsesCreationTimeAndCompletePlacement(t *testing.T) {
	var original SQLInstance
	if err := json.Unmarshal([]byte(`{"name":"database","project":"project","region":"europe-west1","connectionName":"project:europe-west1:database","createTime":"2026-10-21T00:00:00Z"}`), &original); err != nil {
		t.Fatal(err)
	}
	pin, err := original.Incarnation("project", "europe-west1", "database")
	if err != nil || pin == "" {
		t.Fatal("source has no incarnation", err)
	}
	for _, scenario := range []string{"same-instant", "later-creation", "missing-time", "bad-time", "project", "region", "name", "connection"} {
		t.Run(scenario, func(t *testing.T) {
			in := original
			switch scenario {
			case "same-instant":
				in.CreateTime = "2026-10-21T01:00:00+01:00"
			case "later-creation":
				in.CreateTime = "2026-10-21T01:00:00Z"
			case "missing-time":
				in.CreateTime = ""
			case "bad-time":
				in.CreateTime = "yesterday"
			case "project":
				in.Project = "other"
			case "region":
				in.Region = "other"
			case "name":
				in.Name = "other"
			case "connection":
				in.ConnectionName = "other"
			}
			actual, err := in.Incarnation("project", "europe-west1", "database")
			if scenario == "same-instant" {
				if err != nil || actual != pin {
					t.Fatal("equivalent instant changed identity", err)
				}
				return
			}
			if scenario == "later-creation" {
				if err != nil || actual == pin {
					t.Fatal("new creation kept original identity", err)
				}
				return
			}
			if err == nil {
				t.Fatal("incomplete or foreign source accepted")
			}
		})
	}
}

func TestSQLUserConflictChecksIdentityBeforePasswordUpdate(t *testing.T) {
	changed := false
	posts, updates := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			changed = true
			w.WriteHeader(http.StatusConflict)
			return
		}
		updates++
		fmt.Fprint(w, `{"name":"update","status":"DONE"}`)
	}))
	defer srv.Close()
	s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
	changedIdentity := errors.New("cloud sql incarnation changed")
	err := s.SetUser(t.Context(), "instance", "app", "original-password", SQLMutation{
		Before: func() error { return nil }, Started: func(string) error { return nil },
		Check: func(context.Context) error {
			if changed {
				return changedIdentity
			}
			return nil
		},
	})
	if !errors.Is(err, changedIdentity) || posts != 1 || updates != 0 {
		t.Fatalf("replacement received password update: posts=%d updates=%d err=%v", posts, updates, err)
	}
}

func TestSQLMutationChecksIdentityAfterPersistingIntent(t *testing.T) {
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writes++
		fmt.Fprint(w, `{"name":"create","status":"DONE"}`)
	}))
	defer srv.Close()
	s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
	changed := false
	changedIdentity := errors.New("cloud sql incarnation changed")
	err := s.EnsureDatabase(t.Context(), "instance", "database", SQLMutation{
		Before: func() error { changed = true; return nil }, Started: func(string) error { return nil },
		Check: func(context.Context) error {
			if changed {
				return changedIdentity
			}
			return nil
		},
	})
	if !errors.Is(err, changedIdentity) || writes != 0 {
		t.Fatalf("intent persistence race reached provider write: %d %v", writes, err)
	}
}
