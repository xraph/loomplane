package catalogconnect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/recipes"
)

// SearchProber checks product, authenticated identity and cluster-health read access.
// It does not establish write rights, index ownership or application compatibility.
type SearchProber struct{}

var _ Prober = SearchProber{}

const maxSearchBody = 64 * 1024

// Probe uses fixed native requests through the target's supplied dialer.
func (SearchProber) Probe(ctx context.Context, dial DialFunc, raw string, req recipes.Requirement) (*Report, error) {
	a, err := ParseEngineAccess(raw)
	if err != nil {
		return nil, err
	}
	if a.Engine != EngineElasticsearch && a.Engine != EngineOpenSearch || req.Engine != "" && req.Engine != a.Engine || len(req.Extensions) > 0 || len(req.Preload) > 0 || req.CreateRole {
		return nil, invalidEngineAccess()
	}
	if len(req.TLS) > 0 && !slices.Contains(req.TLS, a.TLS) {
		return &Report{Problems: []string{"the search endpoint does not satisfy the required TLS policy"}}, nil
	}
	if dial == nil {
		return nil, errors.New("the search check requires a guarded dialer")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	roots, err := engineRootCAs(a.CACert)
	if err != nil {
		return nil, err
	}
	authority := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
	guardedDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != authority {
			return nil, errors.New("the search check refused a different authority")
		}
		conn, dialErr := dial(ctx, network, address)
		if dialErr != nil {
			return nil, dialErr
		}
		if deadline, ok := ctx.Deadline(); ok {
			if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
				_ = conn.Close()
				return nil, deadlineErr
			}
		}
		return conn, nil
	}
	transport := &http.Transport{Proxy: nil, DialContext: guardedDial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: a.Host}, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16 * 1024, ResponseHeaderTimeout: probeTimeout, TLSHandshakeTimeout: probeTimeout, ForceAttemptHTTP2: false}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	scheme := "https"
	if a.TLS == TLSDisable {
		scheme = "http"
	}
	base := (&url.URL{Scheme: scheme, Host: authority}).String()
	var root struct {
		Version struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
			BuildFlavor  string `json:"build_flavor"`
		} `json:"version"`
		Tagline string `json:"tagline"`
	}
	headers, err := searchRequest(ctx, client, base+"/", a, &root)
	if err != nil {
		return nil, searchFailure(ctx, err)
	}
	parts, versionErr := versionParts(root.Version.Number)
	if versionErr != nil || len(parts) != 3 || len(root.Version.Number) > 32 {
		return nil, refused("the search product version could not be verified")
	}
	switch a.Engine {
	case EngineElasticsearch:
		if headers.Get("X-Elastic-Product") != "Elasticsearch" || root.Version.Distribution != "" || root.Version.BuildFlavor != "default" || root.Tagline != "You Know, for Search" || (parts[0] != 9 || parts[1] != 4) {
			return nil, refused("the endpoint is not a supported Elasticsearch 9.4 product")
		}
	case EngineOpenSearch:
		if headers.Get("X-Elastic-Product") != "" || root.Version.Distribution != "opensearch" || root.Tagline != "The OpenSearch Project: https://opensearch.org/" || (parts[0] != 3 || parts[1] != 9) {
			return nil, refused("the endpoint is not a supported OpenSearch 3.9 product")
		}
	}
	report := &Report{Version: root.Version.Number}
	checkVersion(report, a.Engine, req)
	if len(report.Problems) > 0 {
		return report, nil
	}
	// A successful root response alone does not prove supplied credentials were used.
	if a.Engine == EngineElasticsearch {
		var auth struct {
			Username           string `json:"username"`
			AuthenticationType string `json:"authentication_type"`
		}
		if _, err = searchRequest(ctx, client, base+"/_security/_authenticate", a, &auth); err != nil {
			return nil, searchFailure(ctx, err)
		}
		if auth.Username != a.Username || auth.AuthenticationType != "realm" {
			return nil, refused("the search application identity could not be verified")
		}
	} else {
		var auth struct {
			Username string `json:"user_name"`
		}
		if _, err = searchRequest(ctx, client, base+"/_plugins/_security/authinfo", a, &auth); err != nil {
			return nil, searchFailure(ctx, err)
		}
		if auth.Username != a.Username || auth.Username == "opendistro_security_anonymous" || auth.Username == "opendistro_security_default_transport_user" {
			return nil, refused("the search application identity could not be verified")
		}
	}
	var health struct {
		Status        string `json:"status"`
		TimedOut      *bool  `json:"timed_out"`
		NumberOfNodes int    `json:"number_of_nodes"`
	}
	if _, err = searchRequest(ctx, client, base+"/_cluster/health?local=true&timeout=1s&filter_path=status,timed_out,number_of_nodes", a, &health); err != nil {
		return nil, searchFailure(ctx, err)
	}
	if health.TimedOut == nil || *health.TimedOut || health.NumberOfNodes < 1 || health.Status != "green" && health.Status != "yellow" {
		return nil, refused("the search cluster-health read did not confirm an available cluster")
	}
	return report, nil
}

func searchRequest(ctx context.Context, client *http.Client, target string, a EngineAccess, out any) (http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, errors.New("the fixed search request could not be prepared")
	}
	request.SetBasicAuth(a.Username, a.Password)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, refused("the search application credentials or required read permission were refused")
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" {
		return nil, refused("the fixed search request was refused")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSearchBody+1))
	if err != nil || len(data) > maxSearchBody {
		return nil, refused("the search response exceeded the check bounds or was incomplete")
	}
	if searchJSON(data, out) != nil {
		return nil, refused("the search response could not be verified")
	}
	return response.Header, nil
}

func searchFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errTimedOut
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errors.New("the search check was canceled")
	}
	var pe *probeError
	if errors.As(err, &pe) {
		return pe
	}
	return errors.New("the search check could not establish a verified connection")
}

// searchJSON permits native metadata fields, but bounds nesting and token work.
func searchJSON(data []byte, out any) error {
	if len(data) > maxSearchBody {
		return errors.New("search response limit")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()
	budget := 4096
	if err := searchJSONValue(d, 0, &budget); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("search response trailing data")
	}
	return json.Unmarshal(data, out)
}

func searchJSONValue(d *json.Decoder, depth int, budget *int) error {
	*budget--
	if depth > 12 || *budget < 0 {
		return errors.New("search response complexity limit")
	}
	token, tokenErr := d.Token()
	if tokenErr != nil {
		return tokenErr
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, keyErr := d.Token()
			name, valid := key.(string)
			*budget--
			if keyErr != nil || !valid || seen[strings.ToLower(name)] || searchCaseVariant(name) || *budget < 0 {
				return errors.New("search response duplicate or invalid key")
			}
			seen[strings.ToLower(name)] = true
			if err := searchJSONValue(d, depth+1, budget); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := searchJSONValue(d, depth+1, budget); err != nil {
				return err
			}
		}
	default:
		return errors.New("search response delimiter")
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return errors.New("search response delimiter")
	}
	return nil
}

func searchCaseVariant(name string) bool {
	lower := strings.ToLower(name)
	if lower == name {
		return false
	}
	switch lower {
	case "version", "number", "distribution", "build_flavor", "tagline", "username", "authentication_type", "user_name", "status", "timed_out", "number_of_nodes":
		return true
	}
	return false
}
