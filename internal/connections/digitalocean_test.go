package connections

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/digitalocean"
)

func TestDigitalOceanChecker(t *testing.T) {
	account := `{"account":{"uuid":"acct-1","email":"ada@example.com","status":"active","team":{"uuid":"team-1","name":"Acme"}}}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer dop_v1_team":
			_, _ = w.Write([]byte(account))
		case "Bearer dop_v1_solo":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-2","email":"bob@example.com","status":"active"}}`))
		case "Bearer dop_v1_locked":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-3","email":"eve@example.com","status":"locked"}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"id":"Unauthorized","message":"Unable to authenticate you"}`))
		}
	}))
	defer api.Close()
	c := DigitalOceanChecker{Client: digitalocean.New(api.URL)}

	if _, err := c.Parse([]byte("not a token"), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("parse: %v", err)
	}
	got, err := c.Check(t.Context(), []byte("dop_v1_team"), nil)
	if err != nil || got.Account != "Acme" || got.Details["team_uuid"] != "team-1" || got.Details["account_uuid"] != "acct-1" {
		t.Fatalf("team token: %+v, %v", got, err)
	}
	solo, err := c.Check(t.Context(), []byte("dop_v1_solo"), nil)
	if err != nil || solo.Account != "bob@example.com" || solo.Details["team_uuid"] != "" {
		t.Fatalf("solo token: %+v, %v", solo, err)
	}
	for _, token := range []string{"dop_v1_locked", "dop_v1_revoked"} {
		if _, err := c.Check(t.Context(), []byte(token), nil); !rejected(err) {
			t.Errorf("%s: %v isn't a rejection", token, err)
		}
	}

	// The team is the scope: another member's token for it is the same
	// connection, and the same person's token for another team isn't.
	team := AccountIdentity(DigitalOcean, "Acme", got.Details)
	if team == "" || team != AccountIdentity(DigitalOcean, "Acme renamed", map[string]string{"team_uuid": "team-1", "account_uuid": "acct-9"}) {
		t.Fatal("a team's identity changed with its name or member")
	}
	if team == AccountIdentity(DigitalOcean, "Acme", map[string]string{"team_uuid": "team-2", "account_uuid": "acct-1"}) {
		t.Fatal("two teams share an identity")
	}
	if AccountIdentity(DigitalOcean, "x", map[string]string{}) != "" {
		t.Fatal("an unverified connection has an identity")
	}
}
