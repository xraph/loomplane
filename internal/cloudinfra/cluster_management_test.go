package cloudinfra

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

func qualifyClusterFixture(t *testing.T, s *Service) *httptest.Server {
	return qualifyClusterSwitchableFixture(t, s, nil)
}
func qualifyClusterSwitchableFixture(t *testing.T, s *Service, redirect *atomic.Bool) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect != nil && redirect.Load() {
			http.Redirect(w, r, "https://must-not-follow.invalid/readyz", http.StatusFound)
			return
		}
		if r.URL.Path != "/readyz" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("invalid management probe request")
			w.WriteHeader(400)
			return
		}
		_, _ = fmt.Fprint(w, "ok")
	}))
	t.Cleanup(server.Close)
	pin := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	s.ManagedClusters.ManagementProbe = config.ManagedClusterManagementProbeConfig{Provider: "digitalocean", NetworkID: "network", EvidenceID: "qualification-1", URL: server.URL + "/readyz", SPKISHA256: hex.EncodeToString(pin[:]), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	return server
}
func TestClusterManagementQualificationCannotBeBypassed(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*Service)
	}{
		{"missing", "missing_configuration", func(s *Service) { s.ManagedClusters.ManagementProbe = config.ManagedClusterManagementProbeConfig{} }},
		{"expired", "expired_evidence", func(s *Service) {
			s.ManagedClusters.ManagementProbe.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
		}},
		{"wrong-network", "scope_mismatch", func(s *Service) { s.ManagedClusters.ManagementProbe.NetworkID = "other" }},
		{"wrong-account", "scope_mismatch", func(s *Service) { s.ManagedClusters.QualificationAccountID = "other" }},
		{"wrong-region", "scope_mismatch", func(s *Service) { s.ManagedClusters.QualificationRegion = "other" }},
		{"wrong-provider", "scope_mismatch", func(s *Service) { s.ManagedClusters.ManagementProbe.Provider = "other" }},
		{"userinfo", "invalid_configuration", func(s *Service) { s.ManagedClusters.ManagementProbe.URL = "https://secret@localhost/readyz" }},
		{"query", "invalid_configuration", func(s *Service) { s.ManagedClusters.ManagementProbe.URL += "?secret=value" }},
		{"fragment", "invalid_configuration", func(s *Service) { s.ManagedClusters.ManagementProbe.URL += "#secret" }},
		{"empty-fragment", "invalid_configuration", func(s *Service) { s.ManagedClusters.ManagementProbe.URL += "#" }},
		{"wrong-pin", "unreachable", func(s *Service) { s.ManagedClusters.ManagementProbe.SPKISHA256 = strings.Repeat("0", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, adapter, _, p, in := clusterFixture(t)
			tc.change(s)
			_, _, err := s.probeClusterManagement(t.Context(), p.OrgID, in)
			var preflight *ClusterManagementPreflightError
			if !errors.As(err, &preflight) || preflight.Reason != tc.reason {
				t.Fatalf("got %v, want %s", err, tc.reason)
			}
			if strings.Contains(err.Error(), s.ManagedClusters.ManagementProbe.URL) && s.ManagedClusters.ManagementProbe.URL != "" {
				t.Fatal("probe URL leaked")
			}
			if _, err = s.PreviewCluster(t.Context(), p, "cloud", in); err == nil || adapter.creates != 0 {
				t.Fatal("caller flag bypassed server qualification", err)
			}
		})
	}
}
func TestClusterManagementQualificationRecheckedAndPrivate(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	o, err := s.PreviewCluster(t.Context(), p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	probe := s.ManagedClusters.ManagementProbe
	if o.Input.ManagementEvidenceID != probe.EvidenceID || o.Input.ManagementQualificationDigest == "" || strings.Contains(string(raw), probe.URL) || strings.Contains(string(raw), probe.SPKISHA256) {
		t.Fatal("review missed evidence or exposed probe material")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", probe, probe), probe.URL) {
		t.Fatal("configuration formatting leaked probe")
	}
	s.ManagedClusters.ManagementProbe.EvidenceID = "replacement-evidence"
	if _, err = s.ApplyCluster(t.Context(), p, "cloud", o.ID, ApplyInput{Review: o.Digest}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed qualification accepted", err)
	}
	s.ManagedClusters.ManagementProbe = probe
	if _, err = s.ApplyCluster(t.Context(), p, "cloud", o.ID, ApplyInput{Review: o.Digest}); err != nil {
		t.Fatal(err)
	}
	s.ManagedClusters.ManagementProbe.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	if err = s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.creates != 0 || store.op.Steps[0].State != "pending" || !strings.Contains(store.op.Error, "expired_evidence") {
		t.Fatal("expired evidence permitted purchase")
	}
}
func TestClusterManagementQualificationUnavailableAndRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			s, _, adapter, _, p, in := clusterFixture(t)
			var redirectState atomic.Bool
			server := qualifyClusterSwitchableFixture(t, s, &redirectState)
			o, err := s.PreviewCluster(t.Context(), p, "cloud", in)
			if err != nil {
				t.Fatal(err)
			}
			if redirect {
				redirectState.Store(true)
			} else {
				server.Close()
			}
			if _, err = s.ApplyCluster(t.Context(), p, "cloud", o.ID, ApplyInput{Review: o.Digest}); err == nil || adapter.creates != 0 {
				t.Fatal("unavailable or redirecting management path permitted acceptance", err)
			}
			var preflight *ClusterManagementPreflightError
			if !errors.As(err, &preflight) || (redirect && preflight.Reason != "not_ready") || (!redirect && preflight.Reason != "unreachable") {
				t.Fatal("probe failed without the expected safe reason", err)
			}
		})
	}
}
