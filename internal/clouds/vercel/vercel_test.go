package vercel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckVerifiedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, team, response, wantUser, wantTeam string
		invalid                                  bool
	}{
		{name: "personal user", response: `{"user":{"id":"user_a","username":"renamed"}}`, wantUser: "user_a"},
		{name: "personal missing id", response: `{"user":{"username":"ada"}}`, invalid: true},
		{name: "team", team: "team_a", response: `{"id":"team_a","slug":"team"}`, wantTeam: "team_a"},
		{name: "wrong team", team: "team_a", response: `{"id":"team_b","slug":"team"}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/v2/user"
				if tc.team != "" {
					wantPath = "/v2/teams/" + tc.team
				}
				if r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("identity request: %s", r.URL)
				}
				fmt.Fprint(w, tc.response)
			}))
			defer api.Close()
			account, err := New(api.URL).Check(t.Context(), Token{Value: "token", TeamID: tc.team})
			if tc.invalid {
				if err == nil {
					t.Fatal("response without verified identity was accepted")
				}
				return
			}
			if err != nil || account.UserID != tc.wantUser || account.TeamID != tc.wantTeam {
				t.Fatalf("verified identity: %+v %v", account, err)
			}
		})
	}
}
