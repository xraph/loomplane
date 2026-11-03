package nodes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/node/release"
)

func TestNodeShRendersURLAndVersion(t *testing.T) {
	r := newRig(t)
	// No session: a machine that has nothing yet has to be able to fetch it.
	out := r.do(t, http.MethodGet, "/install/node.sh", "", nil)
	if out.Code != http.StatusOK {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	if ct := out.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/") {
		t.Fatalf("content type %q", ct)
	}
	body := out.Body.String()
	for _, want := range []string{"#!/bin/sh", "set -eu", "LOOMPLANE_URL='" + publicURL + "'", "LOOMPLANE_VERSION='v1.8.0'", `node join --url "$LOOMPLANE_URL" "$@"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the script lacks %q", want)
		}
	}

	r.ext.cfg.PublicURL = publicURL + "/"
	if out = r.do(t, http.MethodGet, "/install/node.sh", "", nil); !strings.Contains(out.Body.String(), "LOOMPLANE_URL='"+publicURL+"'\n") {
		t.Fatalf("with a trailing slash: %s", out.Body)
	}
}

func TestNodeShPinsIdentityLiterally(t *testing.T) {
	r := newRig(t)
	body := r.do(t, http.MethodGet, "/install/node.sh", "", nil).Body.String()
	for _, want := range []string{release.ReleaseIdentity, release.ReleaseIssuer} {
		if !strings.Contains(body, want) {
			t.Errorf("the script does not carry %q", want)
		}
	}
	// A release cut with workflow_dispatch is signed as main, so cosign
	// must be offered that identity too.
	if !strings.Contains(body, release.MainIdentity) {
		t.Error("the script does not carry the identity of a release cut from main")
	}
	if !strings.Contains(body, `--certificate-identity "$identity"`) || !strings.Contains(body, `--certificate-oidc-issuer "$RELEASE_ISSUER"`) || strings.Contains(body, "regexp") {
		t.Error("cosign is not asked for an exact identity and the issuer")
	}
}

func TestNodeShRefusesAValueThatNeedsEscaping(t *testing.T) {
	for name, set := range map[string]func(*rig){
		"a quote in the public URL": func(r *rig) { r.ext.cfg.PublicURL = "https://loom.example.com/'; id; '" },
		"a newline in the URL":      func(r *rig) { r.ext.cfg.PublicURL = "https://loom.example.com/\nid" },
		"no scheme":                 func(r *rig) { r.ext.cfg.PublicURL = "loom.example.com" },
		"a quote in the version":    func(r *rig) { r.ext.svc.Version = "v1'; id; '" },
		"a dev build":               func(r *rig) { r.ext.svc.Version = "" },
	} {
		r := newRig(t)
		set(r)
		out := r.do(t, http.MethodGet, "/install/node.sh", "", nil)
		if out.Code != http.StatusInternalServerError || strings.Contains(out.Body.String(), "#!/bin/sh") {
			t.Errorf("%s: %d %s", name, out.Code, out.Body)
		}
	}
}

func TestNodeShCarriesOnlyVerifiedSums(t *testing.T) {
	r := newRig(t)
	if body := r.do(t, http.MethodGet, "/install/node.sh", "", nil).Body.String(); strings.Contains(body, "want_sum='") {
		t.Fatal("a script served before any verification carries a sum")
	}
	sum := strings.Repeat("ab", 32)
	calls := 0
	r.ext.fetchSums = func(_ context.Context, version string) (map[string]string, error) {
		calls++
		if version != "v1.8.0" {
			t.Errorf("verified %q, not the version this instance runs", version)
		}
		return map[string]string{"linux_amd64": sum}, nil
	}
	if !r.ext.refreshSums(t.Context()) || calls != 1 {
		t.Fatal("the refresh did not verify")
	}
	if body := r.do(t, http.MethodGet, "/install/node.sh", "", nil).Body.String(); !strings.Contains(body, "linux_amd64) want_sum='"+sum+"' ;;") {
		t.Fatalf("the verified sum is not in the script:\n%s", body)
	}
	// A failed refresh keeps what was verified, and serving never calls it.
	r.ext.fetchSums = func(context.Context, string) (map[string]string, error) { return nil, errors.New("no route to GitHub") }
	if r.ext.refreshSums(t.Context()) {
		t.Fatal("a failed refresh reported success")
	}
	before := calls
	if body := r.do(t, http.MethodGet, "/install/node.sh", "", nil).Body.String(); !strings.Contains(body, "want_sum='"+sum+"'") || calls != before {
		t.Fatal("serving the script lost the sums or went to the network")
	}
}
