package cloudinfra

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClusterManagementPreflightError contains only a safe, actionable reason.
// Probe addresses, TLS pins and response bodies never enter this error.
type ClusterManagementPreflightError struct {
	Reason string `json:"reason"`
}

func (e *ClusterManagementPreflightError) Error() string {
	return fmt.Sprintf("management path qualification: %s", e.Reason)
}
func (e *ClusterManagementPreflightError) Unwrap() error {
	switch e.Reason {
	case "unreachable", "not_ready":
		return ErrPending
	case "changed_evidence":
		return ErrConflict
	default:
		return ErrInvalid
	}
}
func managementPreflight(reason string) error {
	return &ClusterManagementPreflightError{Reason: reason}
}

// probeClusterManagement verifies the representative path configured by the
// qualification operator. It cannot prove the future cluster endpoint is ready.
func (s *Service) probeClusterManagement(ctx context.Context, org string, in ClusterInput) (digest, evidenceID string, err error) {
	c := s.ManagedClusters
	probe := c.ManagementProbe
	if probe.Provider == "" || probe.NetworkID == "" || probe.EvidenceID == "" || probe.URL == "" || probe.SPKISHA256 == "" || probe.ExpiresAt == "" {
		return "", "", managementPreflight("missing_configuration")
	}
	if org != c.QualificationOrgID || in.AccountID != c.QualificationAccountID || in.Spec.Region != c.QualificationRegion || probe.Provider != in.Provider || probe.NetworkID == "" || probe.NetworkID != in.Spec.NetworkID || probe.EvidenceID == "" {
		return "", "", managementPreflight("scope_mismatch")
	}
	expires, parseErr := time.Parse(time.RFC3339, probe.ExpiresAt)
	if parseErr != nil || expires.Sub(s.now()) > 24*time.Hour {
		return "", "", managementPreflight("invalid_configuration")
	}
	if !expires.After(s.now()) {
		return "", "", managementPreflight("expired_evidence")
	}
	endpoint, parseErr := url.Parse(probe.URL)
	if parseErr != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || strings.Contains(probe.URL, "#") || endpoint.Fragment != "" || endpoint.EscapedPath() != "/readyz" {
		return "", "", managementPreflight("invalid_configuration")
	}
	pin, parseErr := hex.DecodeString(probe.SPKISHA256)
	if parseErr != nil || len(pin) != sha256.Size {
		return "", "", managementPreflight("invalid_configuration")
	}
	// The trusted operator's exact SPKI pin is the trust anchor. Hostname and
	// certificate lifetime are checked separately; no system-CA fallback is used.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error { //nolint:gosec // Exact operator-configured SPKI pin replaces CA trust and never bypasses identity verification.
		if len(state.PeerCertificates) == 0 {
			return ErrInvalid
		}
		cert := state.PeerCertificates[0]
		hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		if subtle.ConstantTimeCompare(hash[:], pin) != 1 || s.now().Before(cert.NotBefore) || !s.now().Before(cert.NotAfter) {
			return ErrInvalid
		}
		if checkErr := cert.VerifyHostname(endpoint.Hostname()); checkErr != nil {
			return ErrInvalid
		}
		return nil
	}}
	transport := &http.Transport{TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 5 * time.Second, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, http.NoBody)
	if requestErr != nil {
		return "", "", managementPreflight("invalid_configuration")
	}
	response, requestErr := client.Do(request)
	if requestErr != nil {
		return "", "", managementPreflight("unreachable")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 33))
	if readErr != nil || len(body) > 32 || response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		return "", "", managementPreflight("not_ready")
	}
	if !expires.After(s.now()) {
		return "", "", managementPreflight("expired_evidence")
	}
	// Only this digest and the evidence ID enter immutable input. The URL and
	// trust pin remain in server configuration, including on probe failures.
	material := []string{org, in.Provider, in.AccountID, in.Spec.Region, probe.NetworkID, probe.EvidenceID, probe.URL, probe.SPKISHA256, probe.ExpiresAt}
	raw, marshalErr := json.Marshal(material)
	if marshalErr != nil {
		return "", "", managementPreflight("invalid_configuration")
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), probe.EvidenceID, nil
}
func (s *Service) recheckClusterManagement(ctx context.Context, o *ClusterOperation) error {
	digest, evidenceID, err := s.probeClusterManagement(ctx, o.OrgID, o.Input)
	if err != nil {
		return err
	}
	if digest != o.Input.ManagementQualificationDigest || evidenceID != o.Input.ManagementEvidenceID {
		return managementPreflight("changed_evidence")
	}
	return nil
}
