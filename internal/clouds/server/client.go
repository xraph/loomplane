package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client forwards HTTP over the selected server's Docker Unix socket.
type Client struct {
	Config    *Config
	HTTP      *http.Client
	ssh       *ssh.Client
	transport *http.Transport
}

// Dial authenticates without reading SSH config, agents or ambient keys.
func Dial(ctx context.Context, cfg *Config) (*Client, error) {
	if cfg == nil || cfg.Signer == nil || cfg.HostKey == nil {
		return nil, ErrInvalid
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("reach SSH server: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	sshConfig := &ssh.ClientConfig{User: cfg.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(cfg.Signer)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(key.Marshal(), cfg.HostKey.Marshal()) {
			return ErrRejected
		}
		return nil
	}}
	cc, chans, requests, err := ssh.NewClientConn(conn, cfg.Address, sshConfig)
	stop()
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), ErrRejected.Error()) {
			return nil, ErrRejected
		}
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = cc.Close()
		return nil, err
	}
	client := ssh.NewClient(cc, chans, requests)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return client.DialContext(ctx, "unix", cfg.Socket)
	}, ResponseHeaderTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4}
	return &Client{Config: cfg, ssh: client, transport: transport, HTTP: &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Close ends the authenticated transport and all its forwarded connections.
func (c *Client) Close() error {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	if c.ssh != nil {
		return c.ssh.Close()
	}
	return nil
}

// Request addresses only the configured Docker daemon. The caller owns Body.
func (c *Client) Request(ctx context.Context, method, resource string, body io.Reader) (*http.Response, error) {
	return c.RequestHeaders(ctx, method, resource, body, nil)
}

func (c *Client) RequestHeaders(ctx context.Context, method, resource string, body io.Reader, headers http.Header) (*http.Response, error) {
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
	var info struct{ ID, OSType, Architecture string }
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
