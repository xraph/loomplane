package web

import (
	"encoding/json"
	"net/http"
)

// AssetLinksPath is where Android looks for the apps a site vouches for.
const AssetLinksPath = "/.well-known/assetlinks.json"

// WithAndroidApp serves AssetLinksPath, vouching for the Android app named
// pkg and signed by any of fingerprints. Android then opens the links the
// app claims on this host in the app, and the app may use passwords saved
// for this site. Fingerprints are SHA-256, as keytool prints them.
func (e *Extension) WithAndroidApp(pkg string, fingerprints []string) *Extension {
	type target struct {
		Namespace    string   `json:"namespace"`
		PackageName  string   `json:"package_name"`
		Fingerprints []string `json:"sha256_cert_fingerprints"`
	}
	type statement struct {
		Relation []string `json:"relation"`
		Target   target   `json:"target"`
	}
	body, err := json.Marshal([]statement{{
		Relation: []string{"delegate_permission/common.handle_all_urls", "delegate_permission/common.get_login_creds"},
		Target:   target{Namespace: "android_app", PackageName: pkg, Fingerprints: fingerprints},
	}})
	if err != nil {
		// Strings and lists of strings always marshal.
		panic(err)
	}
	e.assetLinks = body
	return e
}

func (e *Extension) serveAssetLinks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Android fetches it when the app is installed or updated, and wants
	// JSON straight back: no redirect, no other content type.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(e.assetLinks) //nolint:errcheck // the client went away; nothing to do
}
