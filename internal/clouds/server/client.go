package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Client forwards HTTP over the selected server's Docker Unix socket.
type Client struct {
	RequireFence bool
	// RequestsGPUs says the work this client runs asks for GPUs, so Check
	// needs the daemon to name its runtimes.
	RequestsGPUs bool
	Config       *Config
	HTTP         *http.Client
	access       HostAccess
	transport    *http.Transport
}

// Close ends the authenticated transport and all its forwarded connections.
func (c *Client) Close() error {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	if c.access != nil {
		return c.access.Close()
	}
	return nil
}

// DialContext opens a TCP connection from the server, through the host
// access Connect pinned, so what's dialed is reached from the server's
// network. It dials only TCP: a Unix socket on the server, such as
// Docker's, isn't for anyone else to reach.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("%w: the server dials only TCP, not %s", ErrInvalid, network)
	}
	if c.access == nil {
		return nil, fmt.Errorf("%w: the server isn't connected", ErrInvalid)
	}
	return c.access.DialContext(ctx, network, address)
}

// Request addresses only the configured Docker daemon. The caller owns Body.
func (c *Client) Request(ctx context.Context, method, resource string, body io.Reader) (*http.Response, error) {
	return c.RequestHeaders(ctx, method, resource, body, nil)
}

func (c *Client) RequestHeaders(ctx context.Context, method, resource string, body io.Reader, headers http.Header) (*http.Response, error) {
	if c.RequireFence && method != http.MethodGet && method != http.MethodHead {
		if _, ok := EffectFenceFrom(ctx); !ok {
			return nil, fmt.Errorf("pooled runtime mutation has no effect fence")
		}
	}
	if !strings.HasPrefix(resource, "/") || strings.HasPrefix(resource, "//") {
		return nil, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+resource, body)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if fence, ok := EffectFenceFrom(ctx); ok && method != http.MethodGet && method != http.MethodHead {
		return c.fencedRequest(ctx, "request", fence, method, resource, body, req.Header)
	}
	return c.HTTP.Do(req)
}

// Check reads daemon identity and build capability without installing software.
func (c *Client) Check(ctx context.Context) (details map[string]string, missing []string, err error) {
	details = maps.Clone(c.Config.Details)
	missing = []string{}
	var version struct{ Version, APIVersion, Os, Arch string }
	if err := c.getJSON(ctx, "/version", &version); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return details, []string{"docker:access"}, nil
	}
	// Podman's Docker-compatible API can leave out the ID and the runtimes.
	var info struct {
		ID, OSType, Architecture string
		Runtimes                 map[string]json.RawMessage
	}
	if err := c.getJSON(ctx, "/info", &info); err != nil {
		return details, []string{"docker:info"}, nil
	}
	architecture := info.Architecture
	// Docker's info endpoint can use uname names, while OCI images and
	// Kubernetes node labels use Go architecture names.
	switch architecture {
	case "aarch64":
		architecture = "arm64"
	case "x86_64":
		architecture = "amd64"
	case "armv7l", "armv6l":
		architecture = "arm"
	}
	details["version"], details["architecture"], details["docker_id"] = version.Version, architecture, info.ID
	if info.OSType != "linux" {
		missing = append(missing, "docker:linux")
	}
	parts := strings.Split(version.APIVersion, ".")
	if len(parts) != 2 {
		missing = append(missing, "docker:build-api")
	} else if major, err := strconv.Atoi(parts[0]); err != nil || major < 1 {
		missing = append(missing, "docker:build-api")
	} else if minor, err := strconv.Atoi(parts[1]); err != nil || (major == 1 && minor < 41) {
		missing = append(missing, "docker:build-api")
	}
	if info.ID == "" || info.Architecture == "" {
		missing = append(missing, "docker:identity")
	}
	if c.RequestsGPUs && len(info.Runtimes) == 0 {
		missing = append(missing, "docker:runtimes")
	}
	return details, missing, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	response, err := c.Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("docker returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
}
