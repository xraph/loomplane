package firecracker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Credential struct{ CA, Certificate, Key string }
type Client struct {
	HTTP             *http.Client
	Endpoint, HostID string
	Details          map[string]string
}

func NewClient(raw []byte, settings map[string]string) (*Client, error) {
	var c Credential
	if err := Decode(bytes.NewReader(raw), &c, MaxControlBytes); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(settings["endpoint"])
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" || !identifier.MatchString(settings["host_id"]) {
		return nil, ErrInvalid
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.CA)) {
		return nil, ErrInvalid
	}
	cert, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
	if err != nil {
		return nil, ErrInvalid
	}
	endpoint.Path = ""
	sum := sha256.Sum256([]byte(c.CA))
	return &Client{Endpoint: endpoint.String(), HostID: settings["host_id"], Details: map[string]string{"endpoint": endpoint.String(), "host_id": settings["host_id"], "host_ca": hex.EncodeToString(sum[:])}, HTTP: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}}, MaxIdleConns: 8, IdleConnTimeout: time.Minute, ResponseHeaderTimeout: 35 * time.Minute}, Timeout: 36 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("host redirects are not permitted") }}}, nil
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, method, path string, in, out any, source io.Reader) error {
	var body io.Reader
	var encoded []byte
	var err error
	if in != nil {
		encoded, err = json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	if source != nil {
		body = source
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if source != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Loomplane-Request", base64.RawURLEncoding.EncodeToString(encoded))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusNotFound:
			return ErrMissing
		case http.StatusConflict:
			return ErrConflict
		case http.StatusBadRequest:
			return ErrInvalid
		default:
			return ErrUnavailable
		}
	}
	return Decode(resp.Body, out, MaxControlBytes)
}
func (c *Client) Check(ctx context.Context) (Capabilities, error) {
	var out Capabilities
	err := c.call(ctx, http.MethodGet, "/v1/capabilities", nil, &out, nil)
	if err != nil {
		return out, err
	}
	if out.Version != ProtocolVersion || out.HostID != c.HostID || !out.Isolation || len(out.RuntimeIdentity) != 64 || (out.Architecture != "amd64" && out.Architecture != "arm64") {
		return out, ErrUnavailable
	}
	c.Details["architecture"] = out.Architecture
	c.Details["ingress_address"] = out.IngressAddress
	c.Details["build_isolation"] = "ready"
	c.Details["runtime_identity"] = out.RuntimeIdentity
	return out, nil
}
func (c *Client) Apply(ctx context.Context, q Request) (Record, error) {
	var out Record
	err := c.call(ctx, http.MethodPost, "/v1/apply", q, &out, nil)
	return out, err
}
func (c *Client) Inspect(ctx context.Context, s Scope) (Record, error) {
	var out Record
	err := c.call(ctx, http.MethodPost, "/v1/inspect", Request{Scope: s}, &out, nil)
	return out, err
}
func (c *Client) Mutate(ctx context.Context, q Request, action string) (Record, error) {
	if action != "stop" && action != "remove" {
		return Record{}, ErrInvalid
	}
	var out Record
	err := c.call(ctx, http.MethodPost, "/v1/"+action, q, &out, nil)
	return out, err
}
func (c *Client) Guest(ctx context.Context, q Request, action string, source io.Reader) (GuestStatus, error) {
	if action != "status" && action != "logs" && action != "source" {
		return GuestStatus{}, ErrInvalid
	}
	var out GuestStatus
	err := c.call(ctx, http.MethodPost, "/v1/"+action, q, &out, source)
	return out, err
}
