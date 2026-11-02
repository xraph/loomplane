package connections

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// azureApp is the sign-in app the fake knows.
var azureApp = oauth.App{ClientID: "77777777-7777-7777-7777-777777777777", ClientSecret: "az-app-secret"}

// azureSignInSecrets are what an Azure sign-in must never show.
var azureSignInSecrets = []string{"az-app-secret", "rt-ada", "fake-token", azuretest.Secret}

// azureAPI is the azuretest fake, whose token endpoint also trades
// code-ada, sent by azureApp with a PKCE verifier, for refresh token
// rt-ada-1 signed in as ada@contoso.com, and renews any rt-ada token,
// rotating it. Either way the access token is the fake's own, so
// Resource Manager takes it as a Contributor's.
func azureAPI(t *testing.T) *azure.Client {
	t.Helper()
	f := azuretest.New(t)
	enc := base64.RawURLEncoding.EncodeToString
	var mu sync.Mutex
	rotated := 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+azuretest.Tenant+"/oauth2/v2.0/token" {
			f.Server.Config.Handler.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form := r.PostForm
		grant := form.Get("grant_type")
		if grant != "authorization_code" && grant != "refresh_token" {
			f.Server.Config.Handler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if form.Get("client_id") != azureApp.ClientID || form.Get("client_secret") != azureApp.ClientSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided."}`))
			return
		}
		switch {
		case grant == "authorization_code" && form.Get("code") == "code-ada" && form.Get("code_verifier") != "":
			claims := `{"oid":"aaaaaaaa-0000-0000-0000-000000000001","tid":"` + azuretest.Tenant + `","preferred_username":"ada@contoso.com"}`
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "fake-token", "refresh_token": "rt-ada-1", "expires_in": 3599,
				"id_token": enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(claims)) + ".sig",
			})
		case grant == "refresh_token" && strings.HasPrefix(form.Get("refresh_token"), "rt-ada-"):
			mu.Lock()
			rotated++
			next := "rt-ada-" + strconv.Itoa(rotated)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fake-token", "refresh_token": next, "expires_in": 3599})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"AADSTS70008: The refresh token has expired."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return azure.New(srv.URL)
}

// azureSignIn lets s sign in to Azure for real against the fake.
func azureSignIn(t *testing.T, s *Service) {
	t.Helper()
	client := azureAPI(t)
	s.Providers[Azure] = client
	s.checkers[Azure] = AzureChecker{Client: client}
	s.ConfigApps[Azure] = azureApp
}

func noAzureSecrets(t *testing.T, what string, v any) {
	t.Helper()
	shown, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range azureSignInSecrets {
		if strings.Contains(string(shown), secret) {
			t.Fatalf("%s carries a secret", what)
		}
	}
}

func TestCompleteAzureSignInLandsOnTheSameIdentityAsAPrincipal(t *testing.T) {
	r := newSignInRig(t)
	azureSignIn(t, r.s)
	principal, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: Azure, Name: "Acme", Credential: azuretest.Secret, Settings: azuretest.Settings()})
	if err != nil {
		t.Fatal(err)
	}
	if principal.AccountIdentity == "" || principal.Method != MethodToken {
		t.Fatalf("principal %+v", principal)
	}

	// Signing in again keeps the connection where it is, whatever is typed.
	state := r.start(t, starter, SignInInput{Cloud: Azure, ConnectionID: principal.ID, Settings: map[string]string{"resource_group": "elsewhere"}})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-ada"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != principal.ID || c.Method != MethodOAuth || c.AccountIdentity != principal.AccountIdentity || c.Account != "Acme production" {
		t.Fatalf("connection %+v", c)
	}
	want := map[string]string{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription, "resource_group": "loomplane",
		"signed_in_as": "ada@contoso.com", "registry_pull": "admin"}
	if !maps.Equal(c.Details, want) {
		t.Fatalf("details %v", c.Details)
	}
	noAzureSecrets(t, "the connection", c)
	var kept oauth.Grant
	if err = json.Unmarshal(r.secrets.kept["org/"+c.ID], &kept); err != nil || kept.RefreshToken != "rt-ada-1" || kept.Account["email"] != "ada@contoso.com" ||
		kept.Account["tenant_id"] != azuretest.Tenant || kept.Account["resource_group"] != "loomplane" {
		t.Fatalf("vault holds %v: %v", kept.Account, err)
	}
	held := string(r.secrets.kept["org/"+c.ID])

	// A sign-in that does reach another resource group is refused, and
	// Vault keeps what it had.
	if err = r.apps.PutSignIn(t.Context(), &PendingSignIn{State: "st-other", OrgID: "org", UserID: starter.UserID, Cloud: Azure, ConnectionID: c.ID,
		Settings: map[string]string{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription, "resource_group": "elsewhere"},
		Verifier: "v", CreatedAt: r.clock, ExpiresAt: r.clock.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_, err = r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: "st-other", Code: "code-ada"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "verified account changed") {
		t.Fatalf("different group: %v", err)
	}
	if string(r.secrets.kept["org/"+c.ID]) != held || r.store.rows[c.ID].Details["resource_group"] != "loomplane" {
		t.Fatalf("the connection changed: %+v", r.store.rows[c.ID])
	}
	noAzureSecrets(t, "the refusal", err.Error())
}

func TestCompleteAzureSignInMakesAConnection(t *testing.T) {
	r := newSignInRig(t)
	azureSignIn(t, r.s)
	state := r.start(t, starter, SignInInput{Cloud: Azure, Name: "Shop", Settings: map[string]string{
		"tenant_id": strings.ToUpper(azuretest.Tenant), "subscription_id": strings.ToUpper(azuretest.Subscription), "client_id": "ignored",
	}})
	c, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code-ada"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Method != MethodOAuth || c.Details["signed_in_as"] != "ada@contoso.com" || c.Details["resource_group"] != "loomplane" ||
		c.Details["subscription_id"] != azuretest.Subscription || c.Details["tenant_id"] != azuretest.Tenant {
		t.Fatalf("connection %+v", c)
	}
	if _, ok := c.Details["client_id"]; ok {
		t.Fatalf("a person's sign-in names a client: %v", c.Details)
	}
	placed := map[string]string{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription, "resource_group": "loomplane"}
	if c.AccountIdentity != AccountIdentity(Azure, "", placed) {
		t.Fatalf("identity %q", c.AccountIdentity)
	}
	noAzureSecrets(t, "the connection", c)

	// A deployment's credential is the handout, which parses with the
	// connection's details and renews itself.
	conn, cred, err := r.s.Credential(t.Context(), "org", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := azure.ParseCredential(cred, conn.Details)
	if err != nil || ac.Username != "ada@contoso.com" || ac.ClientID != azureApp.ClientID || ac.ResourceGroup != "loomplane" {
		t.Fatalf("parse the handout: %v", err)
	}
	session, err := r.s.Providers[Azure].(*azure.Client).Session(t.Context(), ac)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Subscription(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStartAzureSignInChecksThePlacement(t *testing.T) {
	r := newSignInRig(t)
	azureSignIn(t, r.s)
	for _, settings := range []map[string]string{
		nil,
		{"subscription_id": azuretest.Subscription},
		{"tenant_id": azuretest.Tenant},
		{"tenant_id": azuretest.Tenant + "/../x", "subscription_id": azuretest.Subscription},
		{"tenant_id": azuretest.Tenant, "subscription_id": "nope"},
		{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription, "resource_group": "ends-in."},
	} {
		if _, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: Azure, Name: "Shop", Settings: settings}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", settings, err)
		}
	}
	if len(r.apps.signIns) != 0 {
		t.Fatalf("refused sign-ins were recorded: %v", r.apps.signIns)
	}
	in, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: Azure, Name: "Shop", Settings: map[string]string{
		"tenant_id": strings.ToUpper(azuretest.Tenant), "subscription_id": azuretest.Subscription, "resource_group": "shop-prod",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.AuthorizeURL, "/"+azuretest.Tenant+"/oauth2/v2.0/authorize?") || !strings.Contains(in.AuthorizeURL, "code_challenge=") {
		t.Fatalf("authorize url %s", in.AuthorizeURL)
	}
	pending := r.apps.signIns[in.State]
	want := map[string]string{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription, "resource_group": "shop-prod"}
	if pending == nil || !maps.Equal(pending.Settings, want) || pending.Verifier == "" {
		t.Fatalf("pending %+v", pending)
	}
}

func TestPastedAzureSignInIsRefused(t *testing.T) {
	r := newSignInRig(t)
	azureSignIn(t, r.s)
	key := `{"type":"azure_user","client_id":"` + azureApp.ClientID + `","client_secret":"az-app-secret","refresh_token":"rt-ada-1","username":"ada@contoso.com"}`
	settings := map[string]string{"tenant_id": azuretest.Tenant, "subscription_id": azuretest.Subscription}
	_, err := r.s.Create(t.Context(), starter, CreateInput{Cloud: Azure, Name: "Pasted", Credential: key, Settings: settings})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "sign in to Azure") {
		t.Fatalf("create: %v", err)
	}
	noAzureSecrets(t, "the refusal", err.Error())
	if r.secrets.puts != 0 || len(r.store.rows) != 0 {
		t.Fatalf("puts=%d rows=%d", r.secrets.puts, len(r.store.rows))
	}
	if !(AzureChecker{}).IsGrant([]byte(key)) || (AzureChecker{}).IsGrant([]byte(azuretest.Secret)) || (AzureChecker{}).IsGrant([]byte(`{"type":"other"}`)) {
		t.Fatal("IsGrant")
	}
}
