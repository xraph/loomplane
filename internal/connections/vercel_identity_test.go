package connections

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/security"
)

func TestVercelIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, storedID, oldID, newID, oldName, newName string
		oldStatus                                      int
		replacementOnly, refused                       bool
	}{
		{name: "same user renamed", storedID: "user_a", oldID: "user_a", newID: "user_a", oldName: "renamed", newName: "renamed"},
		{name: "different users same username", storedID: "user_a", oldID: "user_b", newID: "user_b", oldName: "ada", newName: "ada", refused: true},
		{name: "legacy upgrade after rename", oldID: "user_a", newID: "user_a", oldName: "renamed", newName: "renamed"},
		{name: "legacy replacement by recycled username", oldID: "user_a", newID: "user_b", oldName: "renamed", newName: "ada", replacementOnly: true, refused: true},
		{name: "legacy old token unavailable", oldStatus: http.StatusServiceUnavailable, newID: "user_a", newName: "ada", refused: true},
		{name: "legacy old token refused", oldStatus: http.StatusForbidden, newID: "user_a", newName: "ada", refused: true},
		{name: "legacy old identity missing", newID: "user_a", oldName: "ada", newName: "ada", refused: true},
		{name: "replacement identity missing", storedID: "user_a", oldID: "user_a", oldName: "ada", newName: "ada", replacementOnly: true, refused: true},
	} {
		for _, operation := range []string{"replace", "recheck"} {
			if tc.replacementOnly && operation == "recheck" {
				continue
			}
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				var calls []string
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v2/user" {
						t.Errorf("unexpected request: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					token := r.Header.Get("Authorization")
					calls = append(calls, token)
					id, username := tc.newID, tc.newName
					if token == "Bearer old" {
						id, username = tc.oldID, tc.oldName
						if tc.oldStatus != 0 {
							w.WriteHeader(tc.oldStatus)
							return
						}
					}
					fmt.Fprintf(w, `{"user":{"id":%q,"username":%q}}`, id, username)
				}))
				defer api.Close()
				details := map[string]string{}
				if tc.storedID != "" {
					details["user_id"] = tc.storedID
				}
				st := &identityStore{row: Connection{ID: "conn_1", OrgID: "org_1", Cloud: Vercel, Account: "ada", Details: details, Status: StatusReady}}
				vault := &identitySecrets{value: []byte("old")}
				svc := NewService(st, vault, map[string]Checker{Vercel: VercelChecker{Client: vercel.New(api.URL)}}, identityAuthz{}, nil, nil)
				var err error
				if operation == "replace" {
					_, err = svc.Replace(t.Context(), security.Principal{OrgID: "org_1"}, "conn_1", ReplaceInput{Credential: "new"})
				} else {
					_, err = svc.Recheck(t.Context(), security.Principal{OrgID: "org_1"}, "conn_1")
				}
				if tc.refused {
					if err == nil {
						t.Fatal("unverified or different user was accepted")
					}
					if vault.puts != 0 || string(vault.value) != "old" || st.writes != 0 || st.row.Account != "ada" || !reflect.DeepEqual(st.row.Details, details) {
						t.Fatalf("rejected check mutated old identity: vault=%+v row=%+v writes=%d", vault, st.row, st.writes)
					}
					if operation == "replace" && tc.storedID == "" && tc.oldStatus != 0 && !reflect.DeepEqual(calls, []string{"Bearer old"}) {
						t.Fatalf("candidate checked before existing token: %v", calls)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if st.row.Details["user_id"] != "user_a" || st.row.Account != "renamed" || st.writes != 1 {
					t.Fatalf("same user identity was not saved: %+v writes=%d", st.row, st.writes)
				}
				if operation == "replace" {
					expected := []string{"Bearer new"}
					if tc.storedID == "" {
						expected = []string{"Bearer old", "Bearer new"}
					}
					if !reflect.DeepEqual(calls, expected) || vault.puts != 1 || string(vault.value) != "new" {
						t.Fatalf("rotation: calls=%v vault=%+v", calls, vault)
					}
				} else if vault.puts != 0 || string(vault.value) != "old" {
					t.Fatalf("recheck changed secret: %+v", vault)
				}
			})
		}
	}
}
