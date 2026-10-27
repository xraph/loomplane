package connections

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// googleSecrets are what a Google sign-in must never show.
var googleSecrets = []string{"ya29.", "1//", "g-secret"}

// googleAPI is a fake Google. Its token endpoint trades code-ada, sent
// with a PKCE verifier, for refresh token 1//rt signed in as
// ada@example.com, renews 1//rt as ya29.fresh, 1//busy as ya29.busy and
// 1//stale as ya29.stale, refuses the app for 1//app, is down for
// 1//outage and refuses any other refresh token. Resource Manager grants
// the bearer of ya29.fresh every permission a person needs but
// cloudsql.instances.delete, on any project, is down for ya29.busy and
// refuses any other bearer.
func googleAPI(t *testing.T) *gcp.Client {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		refuse := func() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
		}
		switch {
		case r.URL.Path == "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			f := r.PostForm
			switch {
			case f.Get("grant_type") == "authorization_code" && f.Get("code") == "code-ada" && f.Get("code_verifier") != "":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "ya29.first", "refresh_token": "1//rt", "expires_in": 3599,
					"id_token": enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"sub":"1078","email":"ada@example.com"}`)) + ".sig",
				})
			case f.Get("grant_type") == "refresh_token" && f.Get("refresh_token") == "1//rt":
				_, _ = w.Write([]byte(`{"access_token":"ya29.fresh","expires_in":3599,"token_type":"Bearer"}`))
			case f.Get("grant_type") == "refresh_token" && (f.Get("refresh_token") == "1//busy" || f.Get("refresh_token") == "1//stale"):
				_, _ = w.Write([]byte(`{"access_token":"ya29.` + strings.TrimPrefix(f.Get("refresh_token"), "1//") + `","expires_in":3599,"token_type":"Bearer"}`))
			case f.Get("refresh_token") == "1//app":
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"The OAuth client was not found."}`))
			case f.Get("refresh_token") == "1//outage":
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"internal_failure"}`))
			default:
				refuse()
			}
		case strings.HasSuffix(r.URL.Path, ":testIamPermissions") && r.Header.Get("Authorization") == "Bearer ya29.busy":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"The service is currently unavailable."}}`))
		case strings.HasSuffix(r.URL.Path, ":testIamPermissions"):
			if r.Header.Get("Authorization") != "Bearer ya29.fresh" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Request had invalid authentication credentials."}}`))
				return
			}
			held := slices.DeleteFunc(slices.Clone(gcp.UserPermissions), func(p string) bool { return p == "cloudsql.instances.delete" })
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": held})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return gcp.New(srv.URL)
}

// googleSignIn lets s sign in to Google Cloud for real against the fake.
func googleSignIn(t *testing.T, s *Service) *gcp.Client {
	t.Helper()
	google := googleAPI(t)
	s.Providers[GCP] = google
	s.checkers[GCP] = GCPChecker{Client: google}
	s.ConfigApps[GCP] = oauth.App{ClientID: "g-id.apps.googleusercontent.com", ClientSecret: "g-secret"}
	return google
}

func noGoogleSecrets(t *testing.T, what string, v any) {
	t.Helper()
	shown, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range googleSecrets {
		if strings.Contains(string(shown), secret) {
			t.Fatalf("%s carries a secret: %s", what, shown)
		}
	}
}

func TestCompleteGoogleSignInChecksThePermissions(t *testing.T) {
	r := newSignInRig(t)
	googleSignIn(t, r.s)
	state := r.start(t, starter, SignInInput{Cloud: GCP, Name: "Shop", Settings: map[string]string{"project_id": "shop-prod"}})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-ada"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusIncomplete || !slices.Equal(c.Missing, []string{"cloudsql.instances.delete"}) || c.Method != MethodOAuth || c.Account != "shop-prod" {
		t.Fatalf("connection %+v", c)
	}
	if c.Details["project_id"] != "shop-prod" || c.Details["signed_in_as"] != "ada@example.com" {
		t.Fatalf("details %v", c.Details)
	}
	if _, ok := c.Details["service_account"]; ok {
		t.Fatalf("a person's sign-in names a service account: %v", c.Details)
	}
	if c.AccountIdentity != AccountIdentity(GCP, "", map[string]string{"project_id": "shop-prod"}) {
		t.Fatalf("identity %q", c.AccountIdentity)
	}
	noGoogleSecrets(t, "the connection", c)
	var kept oauth.Grant
	if err = json.Unmarshal(r.secrets.kept["org/"+c.ID], &kept); err != nil || kept.RefreshToken != "1//rt" || kept.Account["email"] != "ada@example.com" {
		t.Fatalf("vault holds %+v: %v", kept.Account, err)
	}
}

func TestStartGoogleSignInNeedsAProjectID(t *testing.T) {
	r := newSignInRig(t)
	googleSignIn(t, r.s)
	for _, settings := range []map[string]string{nil, {"project_id": "shop"}, {"project_id": "Shop-prod"}} {
		if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: GCP, Name: "Shop", Settings: settings}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", settings, err)
		}
	}
	if len(r.apps.signIns) != 0 {
		t.Fatalf("refused sign-ins were recorded: %v", r.apps.signIns)
	}
}

func TestCompleteSignInRefusesADifferentProject(t *testing.T) {
	r := newSignInRig(t)
	googleSignIn(t, r.s)
	identity := AccountIdentity(GCP, "", map[string]string{"project_id": "shop-prod"})
	r.store.rows["conn_gcp"] = Connection{ID: "conn_gcp", OrgID: "org", Cloud: GCP, Name: "Shop", Method: MethodToken, Account: "shop-prod",
		Details: map[string]string{"project_id": "shop-prod", "service_account": "deployer@shop-prod.iam.gserviceaccount.com"},
		Status:  StatusReady, Generation: 1, AccountIdentity: identity}
	r.secrets.kept["org/conn_gcp"] = []byte(`{"type":"service_account"}`)

	// Signing in again keeps the connection's project, whatever is typed.
	state := r.start(t, starter, SignInInput{Cloud: GCP, ConnectionID: "conn_gcp", Settings: map[string]string{"project_id": "other-prod"}})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-ada"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "conn_gcp" || c.Method != MethodOAuth || c.Details["project_id"] != "shop-prod" || c.AccountIdentity != identity {
		t.Fatalf("connection %+v", c)
	}
	held := string(r.secrets.kept["org/conn_gcp"])

	// A sign-in that does reach another project is refused, and Vault
	// keeps what it had.
	if err = r.apps.PutSignIn(t.Context(), &PendingSignIn{State: "st-other", OrgID: "org", UserID: starter.UserID, Cloud: GCP, ConnectionID: "conn_gcp",
		Settings: map[string]string{"project_id": "other-prod"}, Verifier: "v", CreatedAt: r.clock, ExpiresAt: r.clock.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_, err = r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: "st-other", Code: "code-ada"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "verified account changed") {
		t.Fatalf("different project: %v", err)
	}
	if string(r.secrets.kept["org/conn_gcp"]) != held || r.store.rows["conn_gcp"].Details["project_id"] != "shop-prod" {
		t.Fatalf("the connection changed: %+v", r.store.rows["conn_gcp"])
	}
}

// googleSignedIn stores a ready Google sign-in for shop-prod that renews
// with refresh token rt and expires in an hour, as Google's do.
func googleSignedIn(t *testing.T, r *refreshRig, id, rt string) {
	t.Helper()
	r.store.rows[id] = Connection{ID: id, OrgID: "org", Cloud: GCP, Name: id, Method: MethodOAuth, Account: "shop-prod",
		Details: map[string]string{"project_id": "shop-prod", "signed_in_as": "ada@example.com"}, Status: StatusReady, Generation: 2}
	r.keep(t, id, oauth.Grant{AccessToken: "ya29.old", RefreshToken: rt, Expiry: r.clock.Add(time.Hour),
		Account: map[string]string{"subject": "1078", "email": "ada@example.com", "project_id": "shop-prod"}})
}

// A refresh token revoked in Google's console: the next deployment's
// session is refused, and the background job asks for a new sign-in and
// tells people once.
func TestRefreshDueMarksARevokedGoogleSignIn(t *testing.T) {
	r := newRefreshRig(t)
	google := googleSignIn(t, r.s)
	googleSignedIn(t, r, "conn_gcp", "1//revoked")

	_, cred, err := r.s.Credential(t.Context(), "org", "conn_gcp")
	if err != nil {
		t.Fatal(err)
	}
	key, err := gcp.ParseKey(cred)
	if err != nil {
		t.Fatal(err)
	}
	session, err := google.Session(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Missing(t.Context()); !errors.Is(err, gcp.ErrRejected) {
		t.Fatalf("deployment session: %v", err)
	}

	report, err := r.s.RefreshDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RefreshReport{Lapsed: 1}) {
		t.Fatalf("report %+v", report)
	}
	row := r.store.rows["conn_gcp"]
	if row.Status != StatusSignInNeeded || row.Generation != 2 || !strings.Contains(row.Message, "Sign in again") {
		t.Fatalf("row %+v", row)
	}
	if len(r.lapsed) != 1 || r.lapsed[0].ID != "conn_gcp" {
		t.Fatalf("lapsed %v", r.lapsed)
	}
	noGoogleSecrets(t, "the row", row)
	noGoogleSecrets(t, "the warnings", r.warned)

	// The next run passes it over.
	if report, err = r.s.RefreshDue(t.Context()); err != nil || report != (RefreshReport{}) || len(r.lapsed) != 1 {
		t.Fatalf("again: %+v %v, lapsed %d", report, err, len(r.lapsed))
	}
}

// Google being down, at its token endpoint or its APIs, is no reason to
// ask anyone to sign in again. Google refusing the token mid-session is.
func TestRecheckKeepsASignInThroughAGoogleOutage(t *testing.T) {
	r := newRefreshRig(t)
	googleSignIn(t, r.s)
	for _, rt := range []string{"1//outage", "1//busy"} {
		googleSignedIn(t, r, "conn_gcp", rt)
		_, err := r.s.Recheck(t.Context(), starter, "conn_gcp")
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", rt, err)
		}
		noGoogleSecrets(t, "the error", err.Error())
		if row := r.store.rows["conn_gcp"]; row.Status != StatusReady || row.Message != "" || row.CheckedAt != nil {
			t.Fatalf("%s changed the row: %+v", rt, row)
		}
	}
	if len(r.lapsed) != 0 {
		t.Fatalf("an outage lapsed %v", r.lapsed)
	}

	googleSignedIn(t, r, "conn_gcp", "1//stale")
	c, err := r.s.Recheck(t.Context(), starter, "conn_gcp")
	if err != nil || c.Status != StatusSignInNeeded || len(r.lapsed) != 1 {
		t.Fatalf("refused mid-session: %+v %v, lapsed %d", c, err, len(r.lapsed))
	}
}

// A session whose renewal Google refuses for the app itself records the
// problem on the app, as a renewal does, and says to fix the app rather
// than sign in again with it. People are told once.
func TestRecheckRecordsARefusedAppFromASession(t *testing.T) {
	r := newRefreshRig(t)
	googleSignIn(t, r.s)
	googleSignedIn(t, r, "conn_gcp", "1//app")
	const why = "Loomplane's Google Cloud sign-in app was refused; check its client ID and secret on Cloud sign-in"
	for range 2 {
		c, err := r.s.Recheck(t.Context(), starter, "conn_gcp")
		if err != nil || c.Status != StatusSignInNeeded || c.Message != why || c.Generation != 2 {
			t.Fatalf("recheck %+v: %v", c, err)
		}
	}
	if r.apps.problems["org/"+GCP] != why || len(r.lapsed) != 1 {
		t.Fatalf("problems %v, lapsed %d", r.apps.problems, len(r.lapsed))
	}
	noGoogleSecrets(t, "the row", r.store.rows["conn_gcp"])

	// Azure's sessions are told apart the same way.
	a := newRefreshRig(t)
	azureSignIn(t, a.s)
	a.s.ConfigApps[Azure] = oauth.App{ClientID: azureApp.ClientID, ClientSecret: "rotated-in-azure"}
	a.store.rows["conn_az"] = Connection{ID: "conn_az", OrgID: "org", Cloud: Azure, Name: "Shop", Method: MethodOAuth, Account: "Acme production",
		Details: map[string]string{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription, "resource_group": "loomplane", "signed_in_as": "ada@contoso.com"},
		Status:  StatusReady, Generation: 2}
	a.keep(t, "conn_az", oauth.Grant{AccessToken: "fake-token", RefreshToken: "rt-ada-1", Expiry: a.clock.Add(time.Hour), Account: map[string]string{"email": "ada@contoso.com"}})
	c, err := a.s.Recheck(t.Context(), starter, "conn_az")
	const azureWhy = "Loomplane's Microsoft Azure sign-in app was refused; check its client ID and secret on Cloud sign-in"
	if err != nil || c.Status != StatusSignInNeeded || c.Message != azureWhy || a.apps.problems["org/"+Azure] != azureWhy || len(a.lapsed) != 1 {
		t.Fatalf("azure recheck %+v: %v, problems %v", c, err, a.apps.problems)
	}
	noAzureSecrets(t, "the row", a.store.rows["conn_az"])
}

// What people are told names the cloud as they know it, not by its ID.
func TestLapseMessagesNameTheCloud(t *testing.T) {
	r := newRefreshRig(t)
	googleSignIn(t, r.s)
	googleSignedIn(t, r, "conn_gcp", "1//revoked")
	c, err := r.s.Recheck(t.Context(), starter, "conn_gcp")
	if err != nil {
		t.Fatal(err)
	}
	r.signedIn(t, "conn_do", "at-1", r.clock.Add(time.Hour))
	r.renewer.errs["rt-at-1"] = oauth.ErrRefused
	if _, _, err = r.s.Credential(t.Context(), "org", "conn_do"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("credential: %v", err)
	}
	r.signedIn(t, "conn_quiet", "at-quiet", r.clock.Add(30*24*time.Hour))
	r.renewer.probeErrs = map[string]error{"at-quiet": oauth.ErrRefused}
	if _, err = r.s.RefreshDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ got, want string }{
		{c.Message, "Google Cloud refused the sign-in. Sign in again."},
		{r.store.rows["conn_do"].Message, "DigitalOcean no longer accepts this sign-in. Sign in again."},
		{r.store.rows["conn_quiet"].Message, "DigitalOcean no longer accepts this sign-in. Sign in again."},
		{noAppWhy(Azure), "Microsoft Azure has no sign-in app set up, so Loomplane can't renew this sign-in. Set up the app, then sign in again."},
		{r.s.appRejected(t.Context(), "org", Vercel), "Loomplane's Vercel sign-in app was refused; check its client ID and secret on Cloud sign-in"},
	} {
		if m.got != m.want {
			t.Errorf("got %q, want %q", m.got, m.want)
		}
	}
}

// A self-renewing sign-in needs its app for every handout. Without one it
// asks for a new sign-in, as a renewal does.
func TestGoogleHandoutWithoutAnAppNeedsSignIn(t *testing.T) {
	r := newRefreshRig(t)
	googleSignIn(t, r.s)
	delete(r.s.ConfigApps, GCP)
	googleSignedIn(t, r, "conn_deploy", "1//rt")
	googleSignedIn(t, r, "conn_check", "1//rt")

	_, cred, err := r.s.Credential(t.Context(), "org", "conn_deploy")
	if !errors.Is(err, ErrNotReady) || cred != nil {
		t.Fatalf("credential %q: %v", cred, err)
	}
	c, err := r.s.Recheck(t.Context(), starter, "conn_check")
	if err != nil || c.Status != StatusSignInNeeded {
		t.Fatalf("recheck %+v: %v", c, err)
	}
	for _, id := range []string{"conn_deploy", "conn_check"} {
		row := r.store.rows[id]
		if row.Status != StatusSignInNeeded || !strings.Contains(row.Message, "no sign-in app set up") || row.Generation != 2 {
			t.Fatalf("%s: %+v", id, row)
		}
	}
	if len(r.lapsed) != 2 || r.attached != 0 {
		t.Fatalf("lapsed %v, %d on a cancellable context", r.lapsed, r.attached)
	}
}

func TestPastedGoogleUserKeyIsRefused(t *testing.T) {
	r := newSignInRig(t)
	googleSignIn(t, r.s)
	key := `{"type":"authorized_user","client_id":"g-id.apps.googleusercontent.com","client_secret":"g-secret",
"refresh_token":"1//rt","project_id":"shop-prod","client_email":"ada@example.com"}`
	_, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: GCP, Name: "Pasted", Credential: key})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "sign in") {
		t.Fatalf("create: %v", err)
	}
	if r.secrets.puts != 0 || len(r.store.rows) != 0 {
		t.Fatalf("puts=%d rows=%d", r.secrets.puts, len(r.store.rows))
	}
	if !(GCPChecker{}).IsGrant([]byte(key)) || (GCPChecker{}).IsGrant(gcpKey(t)) {
		t.Fatal("IsGrant")
	}
}
