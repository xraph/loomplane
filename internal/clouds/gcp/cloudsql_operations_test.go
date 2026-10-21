package gcp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudSQLMutationsCheckpointAndResume(t *testing.T) {
	for _, kind := range []string{"instance", "database", "user", "remove"} {
		for _, stop := range []string{"before", "started", "resume", "missing", "foreign", "no-name", "empty-error", "provider-existing", "complete"} {
			t.Run(kind+"/"+stop, func(t *testing.T) {
				writes, polls, before, started := 0, 0, 0, 0
				interrupted := errors.New("checkpoint unavailable")
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "/operations/") {
						polls++
						if stop == "missing" {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						target := "database"
						if stop == "foreign" {
							target = "another"
						}
						fmt.Fprintf(w, `{"name":"original","status":"DONE","targetProject":"project-test","targetId":%q}`, target)
						return
					}
					if r.Method == http.MethodGet {
						if writes == 0 && stop != "resume" && stop != "foreign" && stop != "missing" {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						fmt.Fprint(w, `{"name":"database","state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`)
						return
					}
					writes++
					if stop == "provider-existing" {
						fmt.Fprint(w, `{"status":"existing","targetId":"another","error":{"errors":[{"message":"failed"}]}}`)
						return
					}
					if stop == "empty-error" {
						fmt.Fprint(w, `{"name":"original","status":"DONE","error":{}}`)
						return
					}
					if stop == "no-name" {
						fmt.Fprint(w, `{"status":"DONE"}`)
						return
					}
					fmt.Fprint(w, `{"name":"original","status":"PENDING","targetProject":"project-test","targetId":"database"}`)
				}))
				defer srv.Close()
				s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
				track := SQLMutation{
					Before: func() error {
						before++
						if stop == "before" {
							return interrupted
						}
						return nil
					},
					Started: func(op string) error {
						started++
						if op != "original" {
							t.Errorf("wrong operation: %s", op)
						}
						if stop == "started" {
							return interrupted
						}
						return nil
					},
				}
				if stop == "resume" || stop == "missing" || stop == "foreign" {
					track.Resume = "original"
				}
				var err error
				switch kind {
				case "instance":
					_, err = s.EnsureSQLInstance(t.Context(), SQLInstanceSpec{Name: "database"}, track)
				case "database":
					err = s.EnsureDatabase(t.Context(), "database", "app", track)
				case "user":
					err = s.SetUser(t.Context(), "database", "app", "secret", track)
				case "remove":
					err = s.DeleteSQLInstance(t.Context(), "database", track)
				}
				switch stop {
				case "before", "started":
					if !errors.Is(err, interrupted) {
						t.Fatalf("checkpoint error ignored: %v", err)
					}
					want := 0
					if stop == "started" {
						want = 1
					}
					if writes != want || polls != 0 {
						t.Fatalf("continued after checkpoint error: writes=%d polls=%d", writes, polls)
					}
				case "complete":
					if err != nil || writes != 1 || polls != 1 || before != 1 || started != 1 {
						t.Fatalf("mutation was not tracked: writes=%d polls=%d before=%d started=%d err=%v", writes, polls, before, started, err)
					}
				case "resume":
					if err != nil || writes != 0 || polls != 1 || before != 0 || started != 0 {
						t.Fatalf("resumed mutation replayed: writes=%d polls=%d before=%d started=%d err=%v", writes, polls, before, started, err)
					}
				case "missing", "foreign":
					if err == nil || writes != 0 {
						t.Fatalf("uncertain operation replayed or accepted: writes=%d err=%v", writes, err)
					}
				case "provider-existing":
					if err == nil {
						t.Fatal("malformed provider status was mistaken for an internal completion")
					}
				case "empty-error":
					if err == nil {
						t.Fatal("operation error without details was treated as success")
					}
				case "no-name":
					if err == nil || started != 0 || polls != 0 {
						t.Fatalf("missing operation identity accepted: started=%d polls=%d err=%v", started, polls, err)
					}
				}
			})
		}
	}
}

func TestCloudSQLResumedCreateCannotRecreateMissingInstance(t *testing.T) {
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
			t.Error("recreated an instance after its operation completed")
		}
		if strings.Contains(r.URL.Path, "/operations/") {
			fmt.Fprint(w, `{"name":"original","status":"DONE"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
	_, err := s.EnsureSQLInstance(t.Context(), SQLInstanceSpec{Name: "database"}, SQLMutation{Resume: "original"})
	if err == nil || writes != 0 {
		t.Fatalf("completed operation authorized a new instance: writes=%d err=%v", writes, err)
	}
}

func TestCloudSQLDoesNotAdoptAnUnboundInstance(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("unbound existing instance was mutated")
				}
				fmt.Fprint(w, `{"name":"database","state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
			_, err := s.EnsureSQLInstance(t.Context(), SQLInstanceSpec{Name: "database", Existing: bound}, SQLMutation{})
			if bound && err != nil {
				t.Fatal(err)
			}
			if !bound && err == nil {
				t.Fatal("an unbound existing instance was adopted")
			}
		})
	}
}
