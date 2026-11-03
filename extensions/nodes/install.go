package nodes

import (
	"net/http"

	"github.com/xraph/loomplane/internal/node/install"
	"github.com/xraph/loomplane/internal/node/release"
)

// nodeSh serves the script that installs loomctl on a machine and joins it.
// It needs no session: a new machine has nothing yet, and the script holds
// only this instance's public URL and version, with the release identity it
// must be signed by. The join token comes on the command line.
func (e *Extension) nodeSh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	script, err := install.Render(install.Params{
		PublicURL: e.cfg.PublicURL, Version: e.svc.Version,
		Identity: release.ReleaseIdentity, MainIdentity: release.MainIdentity, Issuer: release.ReleaseIssuer,
		Sums: e.verifiedSums(),
	})
	if err != nil {
		e.Logger().Error("cannot render node.sh: " + err.Error())
		http.Error(w, "this Loomplane cannot make an install script: its public URL or version is not one a script can carry", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(script)) //nolint:errcheck // the client went away; nothing to do
	}
}
