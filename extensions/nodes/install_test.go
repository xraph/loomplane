package nodes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/node/install"
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
	for _, want := range []string{install.ReleaseIdentity, install.ReleaseIssuer} {
		if !strings.Contains(body, want) {
			t.Errorf("the script does not carry %q", want)
		}
	}
	// Both ways of checking name them: cosign here, loomctl via its own pin.
	if !strings.Contains(body, `--certificate-identity "$RELEASE_IDENTITY$tag"`) || !strings.Contains(body, `--certificate-oidc-issuer "$RELEASE_ISSUER"`) {
		t.Error("cosign is not asked for the identity and issuer")
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
