package connections

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/security"
)

var otherManager = security.Principal{OrgID: "org", UserID: "bob"}

func TestCompleteSignInRecordsWhoSignedIn(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SignedInBy != "ada" || r.store.rows[c.ID].SignedInBy != "ada" {
		t.Fatalf("signed in by %q, stored %q", c.SignedInBy, r.store.rows[c.ID].SignedInBy)
	}
}

func TestSignInAgainByAnotherManagerShowsThem(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	first, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	state = r.start(t, otherManager, SignInInput{Cloud: DigitalOcean, ConnectionID: first.ID})
	again, err := r.s.CompleteSignIn(t.Context(), otherManager, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.SignedInBy != "bob" || r.store.rows[first.ID].SignedInBy != "bob" {
		t.Fatalf("connection %+v", again)
	}
	last := len(r.audit.actions) - 1
	if r.audit.actions[last] != "cloud_connection.replace" || r.audit.actors[last] != "bob" || r.audit.signers[last] != "bob" {
		t.Fatalf("audit %v by %v showing %v", r.audit.actions, r.audit.actors, r.audit.signers)
	}
}

func TestReplaceWithATokenForgetsWhoSignedIn(t *testing.T) {
	r := newSignInRig(t)
	state := r.start(t, starter, SignInInput{Cloud: DigitalOcean, Name: "Acme"})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-team-1"})
	if err != nil {
		t.Fatal(err)
	}
	// A pasted token isn't a person's sign-in, whoever pasted it.
	got, err := r.s.Replace(t.Context(), otherManager, c.ID, ReplaceInput{Credential: "dop_v1_team"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != MethodToken || got.SignedInBy != "" || r.store.rows[c.ID].SignedInBy != "" {
		t.Fatalf("connection %+v", got)
	}
}

// A person leaving asks for a new sign-in on the clouds that act as them,
// once, and leaves the rest alone.
func TestRefreshDueAsksAgainWhenTheSignerLeaves(t *testing.T) {
	r := newRefreshRig(t)
	googleSignIn(t, r.s)
	googleSignedIn(t, r, "conn_gcp", "1//rt")
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(48*time.Hour))
	for _, id := range []string{"conn_gcp", "conn_do"} {
		row := r.store.rows[id]
		row.SignedInBy = "ada"
		r.store.rows[id] = row
	}
	r.locked.departed = map[string]bool{"ada": true}

	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Left != 1 {
		t.Fatalf("report %+v", report)
	}
	gcp, do := r.store.rows["conn_gcp"], r.store.rows["conn_do"]
	if gcp.Status != StatusSignInNeeded || gcp.Generation != 2 ||
		gcp.Message != "The person who signed in to conn_gcp has left the organization. Sign in again." {
		t.Fatalf("google %+v", gcp)
	}
	if do.Status != StatusReady {
		t.Fatalf("digitalocean %+v", do)
	}
	if len(r.lapsed) != 1 || r.lapsed[0].ID != "conn_gcp" {
		t.Fatalf("lapsed %v", r.lapsed)
	}
	if report, err = r.s.RefreshDue(t.Context()); err != nil || report.Left != 0 || len(r.lapsed) != 1 {
		t.Fatalf("again: %+v %v, lapsed %d", report, err, len(r.lapsed))
	}
	if !slices.Equal(personalClouds, []string{GCP, Azure}) {
		t.Fatalf("personal clouds %v", personalClouds)
	}
}

func TestRefreshDueRenewsWhenTheMembershipCheckFails(t *testing.T) {
	r := newRefreshRig(t)
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.grants["rt-at-1"] = oauth.Grant{AccessToken: "at-2", RefreshToken: "rt-at-2", Expiry: r.clock.Add(30 * 24 * time.Hour)}
	r.s.grants = brokenMembers{r.locked}

	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{Refreshed: 1, Failed: 1}) {
		t.Fatalf("report %+v", report)
	}
	if got := r.kept(t, "conn_do").AccessToken; got != "at-2" {
		t.Fatalf("the due grant wasn't renewed: %q", got)
	}
	if len(r.warned) != 1 || !strings.Contains(r.warned[0], "unreachable") {
		t.Fatalf("warned %v", r.warned)
	}
}
