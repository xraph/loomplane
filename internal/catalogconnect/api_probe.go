package catalogconnect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/xraph/loomplane/internal/recipes"
)

// APIProber validates application access against the pinned provider resource.
// Native bindings validate the descriptor only; Worker deployment verifies the provider identity.
type APIProber struct{ RootCAs *x509.CertPool }

var _ Prober = APIProber{}

// Probe uses fixed HTTPS origins and the supplied deployment network dialer.
func (p APIProber) Probe(ctx context.Context, dial DialFunc, raw string, req recipes.Requirement) (*Report, error) {
	a, err := ParseAPIAccess(raw)
	if err != nil {
		return nil, err
	}
	if req.Engine != "" && req.Engine != a.Kind {
		return nil, invalidAPIAccess()
	}
	if req.MinVersion != "" || len(req.Extensions) > 0 || len(req.Preload) > 0 || req.CreateRole {
		return &Report{Problems: []string{"provider API access cannot satisfy database version, extension or role requirements"}}, nil
	}
	if a.Native {
		return &Report{Version: "native-binding"}, nil
	}

	if dial == nil {
		return nil, errors.New("the API access check requires a guarded dialer")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.RootCAs}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: probeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if a.Kind == "vercel-integration" {
		err = probeIntegrationAccess(ctx, client, dial, a)
	} else {
		err = probeProviderAPI(ctx, client, a)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errTimedOut
		}
		if a.Kind == "vercel-integration" {
			return &Report{Problems: []string{err.Error()}}, nil
		}
		return nil, errors.New("the application API access check failed; verify its token, resource identity and network")
	}
	return &Report{Version: a.Kind}, nil
}
func probeProviderAPI(ctx context.Context, client *http.Client, a APIAccess) error {
	path := ""
	query := url.Values{}
	switch a.Kind {
	case "cloudflare-d1":
		path = "/client/v4/accounts/" + a.AccountID + "/d1/database/" + url.PathEscape(a.ResourceID)
	case "cloudflare-kv":
		path = "/client/v4/accounts/" + a.AccountID + "/storage/kv/namespaces/" + url.PathEscape(a.ResourceID)
	case "cloudflare-queue":
		path = "/client/v4/accounts/" + a.AccountID + "/queues/" + url.PathEscape(a.ResourceID)
	case "cloudflare-vectorize":
		name := a.ResourceID
		if a.Name != "" {
			name = a.Name
		}
		path = "/client/v4/accounts/" + a.AccountID + "/vectorize/v2/indexes/" + url.PathEscape(name)
	case "vercel-blob":
		path = "/api/blob"
		query.Set("limit", "1")
	case "vercel-global-config":
		path = "/" + a.ResourceID + "/digest"
		query.Set("version", "1")
	default:
		return invalidAPIAccess()
	}
	target := url.URL{Scheme: "https", Host: apiHost(a), Path: path, RawQuery: query.Encode()}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.Token)
	request.Header.Set("Accept", "application/json")
	if a.Kind == "vercel-blob" {
		request.Header.Set("X-Api-Version", "13")
		request.Header.Set("X-Vercel-Blob-Store-Id", blobTokenStore(a.Token))
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("provider API request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("provider API access was refused")
	}
	body, err := readBucketBody(response.Body)
	if err != nil {
		return err
	}
	switch a.Kind {
	case "vercel-blob":
		var listed struct {
			Blobs   *[]json.RawMessage `json:"blobs"`
			HasMore *bool              `json:"hasMore"`
		}
		if json.Unmarshal(body, &listed) != nil || listed.Blobs == nil || listed.HasMore == nil {
			return errors.New("blob listing could not be verified")
		}
	case "vercel-global-config":
		var digest string
		if json.Unmarshal(body, &digest) != nil || digest == "" || len(digest) > 256 {
			return errors.New("global config digest could not be verified")
		}
	default:
		var answer struct {
			Success bool `json:"success"`
			Result  struct {
				ID      string `json:"id"`
				UUID    string `json:"uuid"`
				QueueID string `json:"queue_id"`
				Name    string `json:"name"`
			} `json:"result"`
		}
		if json.Unmarshal(body, &answer) != nil || !answer.Success {
			return errors.New("provider API response could not be verified")
		}
		identity := answer.Result.ID
		switch a.Kind {
		case "cloudflare-d1":
			identity = answer.Result.UUID
		case "cloudflare-queue":
			identity = answer.Result.QueueID
		case "cloudflare-vectorize":
			identity = answer.Result.Name
		}
		expected := a.ResourceID
		if a.Kind == "cloudflare-vectorize" && a.Name != "" {
			expected = a.Name
		}
		if identity != expected {
			return errors.New("provider API resource did not match the selected identity")
		}
	}
	return nil
}
