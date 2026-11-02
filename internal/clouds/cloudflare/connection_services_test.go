package cloudflare

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

func TestSharedBearerBoundaryAcceptsOAuthAndRejectsOversizedTokens(t *testing.T) {
	if err := CheckToken(strings.Repeat("x", 1024)); err != nil {
		t.Fatalf("native resource APIs reject an OAuth bearer: %v", err)
	}
	if err := CheckToken(strings.Repeat("x", (16<<10)+1)); err == nil {
		t.Fatal("unbounded bearer accepted")
	}
}

func TestOAuthR2ScopeDoesNotRequireDNS(t *testing.T) {
	for _, tc := range []struct{ services, want string }{
		{"r2", "account-settings.read workers-r2.write offline_access"},
		{"dns,r2", "account-settings.read zone.read dns.write workers-r2.write offline_access"},
	} {
		u, err := url.Parse(New("", false).AuthorizeURL(oauth.App{}, oauth.AuthorizeRequest{Settings: map[string]string{"services": tc.services}}))
		if err != nil || u.Query().Get("scope") != tc.want {
			t.Fatalf("services %s: %s %v", tc.services, u, err)
		}
	}
}

func TestR2ReadPermissionRequiresAValidBucketList(t *testing.T) {
	for _, tc := range []struct {
		body   string
		code   int
		want   PermissionState
		failed bool
	}{
		{`{"success":true,"result":{"buckets":[]}}`, 200, Granted, false},
		{`{"success":false,"errors":[{"code":10000}]}`, 403, Missing, false},
		{`{"success":false,"errors":[{"code":10000}]}`, 401, Unchecked, true},
		{`{"success":true,"result":{}}`, 200, Unchecked, true},
		{`not-json`, 200, Unchecked, true},
		{`{"success":false}`, 503, Unchecked, true},
	} {
		t.Run(fmt.Sprintf("%d-%s", tc.code, tc.body), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/accounts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/r2/buckets" || r.Header.Get("Authorization") != "Bearer storage-token" {
					t.Error("unsafe permission probe")
				}
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			defer api.Close()
			got, err := New(api.URL, false).R2ReadPermission(t.Context(), "storage-token", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if got != tc.want || (err != nil) != tc.failed {
				t.Fatalf("%s %v", got, err)
			}
		})
	}
}
