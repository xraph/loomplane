package digitalocean

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.ClusterBootstrap = (*Client)(nil)

// BootstrapCluster reads temporary credentials without persisting their material.
func (c *Client) BootstrapCluster(ctx context.Context, access provider.AccountAccess, ref provider.ClusterRef, ttl time.Duration) (provider.BootstrapAccess, error) {
	var empty provider.BootstrapAccess
	if err := clusterRef(access, ref); err != nil {
		return empty, err
	}
	if ttl < time.Second || ttl > time.Hour {
		return empty, provider.ErrInfrastructureInvalid
	}
	if err := c.clusterIdentity(ctx, access); err != nil {
		return empty, err
	}
	if ref.Incarnation != "" || ref.Region != "" {
		if _, err := c.GetCluster(ctx, access, ref); err != nil {
			return empty, err
		}
	}
	var response struct {
		Server      string    `json:"server"`
		CA          string    `json:"certificate_authority_data"`
		Token       string    `json:"token"`
		ExpiresAt   time.Time `json:"expires_at"`
		Certificate string    `json:"client_certificate_data"`
		Key         string    `json:"client_key_data"`
	}
	requestedAt := time.Now()
	q := url.Values{"expiry_seconds": {strconv.FormatInt(int64(ttl/time.Second), 10)}}
	if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID)+"/credentials", q, nil, &response); err != nil {
		return empty, infrastructureError(err, "kubernetes:access_cluster")
	}
	endpoint, err := url.Parse(response.Server)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return empty, provider.ErrInfrastructureInvalid
	}
	ca, err := base64.StdEncoding.DecodeString(response.CA)
	if err != nil || len(ca) == 0 || !x509.NewCertPool().AppendCertsFromPEM(ca) {
		return empty, provider.ErrInfrastructureInvalid
	}
	if response.Token != "" && (strings.TrimSpace(response.Token) == "" || strings.ContainsAny(response.Token, "\r\n\t ")) {
		return empty, provider.ErrInfrastructureInvalid
	}
	out := provider.BootstrapAccess{Endpoint: response.Server, CAData: ca, Token: response.Token, ExpiresAt: response.ExpiresAt}
	if response.Token == "" {
		out.ClientCertificate, err = base64.StdEncoding.DecodeString(response.Certificate)
		if err != nil {
			return empty, provider.ErrInfrastructureInvalid
		}
		out.ClientKey, err = base64.StdEncoding.DecodeString(response.Key)
		if err != nil || len(out.ClientKey) == 0 {
			return empty, provider.ErrInfrastructureInvalid
		}
		if _, pairErr := tls.X509KeyPair(out.ClientCertificate, out.ClientKey); pairErr != nil {
			return empty, provider.ErrInfrastructureInvalid
		}
		block, _ := pem.Decode(out.ClientCertificate)
		if block == nil {
			return empty, provider.ErrInfrastructureInvalid
		}
		cert, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil || time.Now().Before(cert.NotBefore) {
			return empty, provider.ErrInfrastructureInvalid
		}
		out.ExpiresAt = cert.NotAfter
	}
	if !out.ExpiresAt.After(time.Now()) || out.ExpiresAt.After(requestedAt.Add(ttl+5*time.Second)) {
		return empty, provider.ErrInfrastructureInvalid
	}
	return out, nil
}
