package connections

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
)

// cloudflareFixture answers the calls a Cloudflare check makes: Zone Read
// is granted, DNS Write and Registrar write are not.
type cloudflareFixture struct {
	*httptest.Server
	account string
	status  string
}

func newCloudflareFixture(t *testing.T) *cloudflareFixture {
	t.Helper()
	f := &cloudflareFixture{account: strings.Repeat("c", 32), status: "active"}
	reply := func(w http.ResponseWriter, code int, result any) {
		w.WriteHeader(code)
		if code >= 300 {
			fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") != "Bearer tok-secret":
			reply(w, http.StatusUnauthorized, nil)
		case r.URL.Path == "/accounts/"+f.account+"/tokens/verify":
			reply(w, http.StatusOK, map[string]any{"id": "tok1", "status": f.status})
		case r.URL.Path == "/accounts/"+f.account:
			reply(w, http.StatusOK, map[string]any{"id": f.account, "name": "Acme"})
		case r.URL.Path == "/zones":
			reply(w, http.StatusOK, []any{map[string]any{"id": "zone1"}})
		case r.URL.Path == "/zones/zone1/dns_records":
			reply(w, http.StatusForbidden, nil)
		case strings.HasSuffix(r.URL.Path, "/registrations") && r.Method == http.MethodGet:
			reply(w, http.StatusOK, []any{})
		case strings.HasSuffix(r.URL.Path, "/registrations"):
			reply(w, http.StatusForbidden, nil)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestCloudflareCheckerReportsMissingAndKeepsSecretsOut(t *testing.T) {
	f := newCloudflareFixture(t)
	c, err := CloudflareChecker{Client: cloudflare.New(f.URL, false)}.Check(t.Context(), []byte("tok-secret"), map[string]string{"account_id": f.account})
	if err != nil || !slices.Equal(c.Missing, []string{"DNS Write"}) || c.Details["account_id"] != f.account || c.Details["registrar"] != "missing" || c.Account != "Acme" {
		t.Fatalf("%+v %v", c, err)
	}
	if c.Details["zone_read"] != "granted" || c.Details["dns_write"] != "missing" || c.Details["token_id"] != "tok1" || c.Details["registrar_mode"] != "production" {
		t.Fatalf("%+v", c.Details)
	}
	if strings.Contains(fmt.Sprint(c), "tok-secret") {
		t.Fatal("token in check")
	}
	sandbox, err := CloudflareChecker{Client: cloudflare.New(f.URL, true)}.Check(t.Context(), []byte("tok-secret"), map[string]string{"account_id": f.account})
	if err != nil || sandbox.Details["registrar_mode"] != "sandbox" {
		t.Fatalf("%+v %v", sandbox, err)
	}
}

func TestCloudflareCheckerRejectsBadAccountID(t *testing.T) {
	for _, id := range []string{"Acme Inc", "", strings.Repeat("C", 32), strings.Repeat("c", 31)} {
		_, err := CloudflareChecker{}.Parse([]byte("tok"), map[string]string{"account_id": id})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

func TestCloudflareCheckerRejectsRefusedAndInactiveTokens(t *testing.T) {
	f := newCloudflareFixture(t)
	checker := CloudflareChecker{Client: cloudflare.New(f.URL, false)}
	settings := map[string]string{"account_id": f.account}
	if _, err := checker.Check(t.Context(), []byte("wrong"), settings); !rejected(err) {
		t.Fatalf("refused token: %v", err)
	}
	f.status = "expired"
	if _, err := checker.Check(t.Context(), []byte("tok-secret"), settings); !rejected(err) {
		t.Fatalf("inactive token: %v", err)
	}
}

func TestCloudflareAccountIdentityIsTheAccountID(t *testing.T) {
	id := strings.Repeat("c", 32)
	got := AccountIdentity(Cloudflare, "Acme", map[string]string{"account_id": id})
	if got == "" || got != AccountIdentity(Cloudflare, "Acme renamed", map[string]string{"account_id": id, "token_id": "other"}) {
		t.Fatal("identity changed with the account name or token")
	}
	if got == AccountIdentity(Cloudflare, "Acme", map[string]string{"account_id": strings.Repeat("d", 32)}) {
		t.Fatal("two accounts share an identity")
	}
	if AccountIdentity(Cloudflare, "Acme", map[string]string{}) != "" {
		t.Fatal("an unverified connection has an identity")
	}
}
