package cloudinfra

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type SSHRuntime struct{}

func (SSHRuntime) Inspect(ctx context.Context, raw []byte, settings map[string]string, vacant bool) (RuntimeHealth, error) {
	cfg, err := server.ParseConfig(raw, settings)
	if err != nil {
		return RuntimeHealth{}, err
	}
	client, err := server.Dial(ctx, cfg)
	if err != nil {
		return RuntimeHealth{}, err
	}
	defer client.Close()
	details, missing, err := client.Check(ctx)
	if err != nil {
		return RuntimeHealth{}, err
	}
	if len(missing) > 0 {
		return RuntimeHealth{}, fmt.Errorf("docker runtime checks failed: %s", strings.Join(missing, ", "))
	}
	if vacant {
		if checkErr := client.CheckVacant(ctx); checkErr != nil {
			return RuntimeHealth{}, checkErr
		}
	}
	cpus, memory, err := client.Capacity(ctx)
	if err != nil {
		return RuntimeHealth{}, err
	}
	return RuntimeHealth{DockerID: details["docker_id"], Architecture: details["architecture"], CPUs: cpus, MemoryBytes: memory, CheckedAt: time.Now().UTC()}, nil
}

// Enrollment contains only the host's public key. The bearer token is accepted
// from the Authorization header and never recorded in operations or logs.
type Enrollment struct {
	HostKey string `json:"host_key"`
}
type bootstrapSecret struct {
	PrivateKey string `json:"private_key"`
	Token      string `json:"token"`
}

func (*bootstrapSecret) String() string   { return "[redacted bootstrap secret]" }
func (*bootstrapSecret) GoString() string { return "[redacted bootstrap secret]" }
func bootstrapKey(id string) string       { return "infrastructure/" + id }
func (s *Service) validateEnrollmentURL() error {
	u, err := url.Parse(s.EnrollmentURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: configure an HTTPS infrastructure enrollment URL", ErrInvalid)
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = local || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
	}
	if s.AllowLocalEnrollment && (u.Scheme == "http" || u.Scheme == "https") {
		return nil
	}
	if u.Scheme != "https" || local {
		return fmt.Errorf("%w: managed servers need a reachable HTTPS enrollment endpoint", ErrInvalid)
	}
	return nil
}
func (s *Service) prepareBootstrap(ctx context.Context, o *Operation) (string, error) {
	if checkErr := s.validateEnrollmentURL(); checkErr != nil {
		return "", checkErr
	}
	var secret bootstrapSecret
	raw, err := s.Secrets.Reveal(ctx, o.OrgID, bootstrapKey(o.ID))
	if errors.Is(err, connections.ErrNotFound) {
		_, private, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return "", genErr
		}
		encoded, genErr := x509.MarshalPKCS8PrivateKey(private)
		if genErr != nil {
			return "", genErr
		}
		secret.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
		secret.Token, genErr = reviewToken()
		if genErr != nil {
			return "", genErr
		}
		raw, err = json.Marshal(secret) //nolint:gosec // serialized only for encrypted Vault storage
		if err != nil {
			return "", err
		}
		if checkErr := s.Secrets.Put(ctx, o.OrgID, bootstrapKey(o.ID), raw); checkErr != nil {
			return "", checkErr
		}
	} else if err != nil {
		return "", err
	} else if checkErr := json.Unmarshal(raw, &secret); checkErr != nil {
		return "", checkErr
	}
	signer, err := ssh.ParsePrivateKey([]byte(secret.PrivateKey))
	if err != nil {
		return "", err
	}
	o.BootstrapExpiresAt = s.now().UTC().Add(45 * time.Minute)
	// JSON is valid YAML. User data contains a public client key and the one-use
	// enrollment token, never the private SSH key. Cloud-init deletes its token file.
	endpoint := strings.TrimRight(s.EnrollmentURL, "/") + "/api/v1/infrastructure/enroll/" + url.PathEscape(o.OrgID) + "/" + url.PathEscape(o.ID)
	script := `#!/usr/bin/python3
import json, pathlib, time, urllib.request
key = pathlib.Path('/etc/ssh/ssh_host_ed25519_key.pub').read_text().strip()
body = json.dumps({'host_key': key}).encode()
for attempt in range(180):
    try:
        request = urllib.request.Request(ENDPOINT, data=body, headers={'Authorization': 'Bearer '+TOKEN, 'Content-Type':'application/json'})
        with urllib.request.urlopen(request, timeout=10) as response:
            if response.status == 204:
                pathlib.Path(__file__).unlink(missing_ok=True)
                break
    except Exception:
        time.sleep(10)
`
	ep, err := json.Marshal(endpoint)
	if err != nil {
		return "", err
	}
	token, err := json.Marshal(secret.Token)
	if err != nil {
		return "", err
	}
	script = strings.ReplaceAll(strings.ReplaceAll(script, "ENDPOINT", string(ep)), "TOKEN", string(token))
	document := map[string]any{"package_update": true, "groups": []string{"docker"}, "packages": []string{"docker.io", "python3", "iproute2"}, "users": []any{"default", map[string]any{"name": "loomplane", "groups": []string{"docker"}, "shell": "/bin/bash", "ssh_authorized_keys": []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}}}, "write_files": []any{map[string]any{"path": "/root/loomplane-enroll.py", "permissions": "0600", "content": script}}, "runcmd": []any{[]string{"systemctl", "enable", "--now", "docker"}, []string{"python3", "/root/loomplane-enroll.py"}}}
	encoded, err := json.Marshal(document)
	return "#cloud-config\n" + string(encoded), err
}
func (s *Service) bootstrapSecret(ctx context.Context, o *Operation) (*bootstrapSecret, error) {
	raw, err := s.Secrets.Reveal(ctx, o.OrgID, bootstrapKey(o.ID))
	if err != nil {
		return nil, err
	}
	var secret bootstrapSecret
	if checkErr := json.Unmarshal(raw, &secret); checkErr != nil {
		return nil, checkErr
	}
	return &secret, nil
}
func runtimeSettings(o *Operation) (map[string]string, error) {
	if len(o.PublicIPv4) == 0 || net.ParseIP(o.PublicIPv4[0]) == nil || o.HostKey == "" {
		return nil, ErrPending
	}
	return map[string]string{"host": o.PublicIPv4[0], "port": "22", "user": "loomplane", "host_key": o.HostKey, "docker_socket": "/var/run/docker.sock"}, nil
}

// Enroll accepts one verified callback bound to a persisted provider resource.
// An attacker-supplied callback address is never used as the SSH destination.
func (s *Service) Enroll(ctx context.Context, org, id, token string, in Enrollment) error {
	if token == "" || len(in.HostKey) > 8192 {
		return ErrInvalid
	}
	return s.Store.WithOperationLock(ctx, id, func() error {
		o, err := s.Store.OperationByID(ctx, org, id)
		if err != nil {
			return err
		}
		if o.Input.Kind != "create" || o.ResourceID == "" || !o.Attempted || o.Enrolled || !s.now().Before(o.BootstrapExpiresAt) || (o.Status != "pending" && o.Status != "running" && o.Status != "waiting") {
			return ErrConflict
		}
		if authErr := s.Authz.Require(ctx, o.Principal, security.OrganizationCloudManage, o.OrgID); authErr != nil {
			return authErr
		}
		secret, err := s.bootstrapSecret(ctx, o)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(secret.Token)) != 1 {
			return ErrInvalid
		}
		conn, raw, err := s.validateConnection(ctx, o)
		if err != nil {
			return err
		}
		credential, err := Credential(conn, raw)
		if err != nil {
			return err
		}
		adapter, err := s.Adapters.Server(o.Provider, provider.ServerDiscovery)
		if err != nil {
			return err
		}
		remote, err := adapter.GetServer(ctx, credential, o.ResourceID)
		if err != nil {
			return err
		}
		if remote.ID != o.ResourceID || remote.AccountID != o.AccountID {
			return provider.ErrInfrastructureIdentity
		}
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(in.HostKey))
		if err != nil || strings.TrimSpace(string(rest)) != "" {
			return ErrInvalid
		}
		o.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		o.PublicIPv4 = remote.PublicIPv4
		settings, err := runtimeSettings(o)
		if err != nil {
			return err
		}
		if _, err = s.Runtime.Inspect(ctx, []byte(secret.PrivateKey), settings, true); err != nil {
			return err
		}
		o.Enrolled = true
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
		// The durable enrolled checkpoint consumes the token, even if the Vault
		// cleanup write fails. Existing credentials stay usable after recovery.
		secret.Token = ""
		encoded, err := json.Marshal(secret) //nolint:gosec // serialized only for encrypted Vault storage
		if err != nil {
			return err
		}
		return s.Secrets.Put(ctx, o.OrgID, bootstrapKey(o.ID), encoded)
	})
}
func (s *Service) readyRuntime(ctx context.Context, o *Operation, v *Server) error {
	var raw []byte
	var settings map[string]string
	if o.Input.Kind == "create" {
		secret, err := s.bootstrapSecret(ctx, o)
		if err != nil {
			return err
		}
		raw = []byte(secret.PrivateKey)
		settings, err = runtimeSettings(o)
		if err != nil {
			return err
		}
	} else {
		conn, key, err := s.Connections.Credential(ctx, o.OrgID, v.RuntimeConnectionID)
		if err != nil {
			return err
		}
		raw = key
		settings = conn.Details
	}
	health, err := s.Runtime.Inspect(ctx, raw, settings, false)
	if err != nil {
		return ErrPending
	}
	if v.Health.DockerID != "" && health.DockerID != v.Health.DockerID {
		return provider.ErrInfrastructureIdentity
	}
	if o.Input.Kind == "create" {
		// Stable connection ID allows recovery after a lost database response.
		connectionID := "conn_" + strings.TrimPrefix(o.ServerID, "server_")
		if checkErr := s.Secrets.Put(ctx, o.OrgID, connectionID, raw); checkErr != nil {
			return checkErr
		}
		now := s.now().UTC()
		connection := &connections.Connection{ID: connectionID, OrgID: o.OrgID, Cloud: connections.Server, Name: "Server " + o.ServerID, Account: health.DockerID, Details: settings, Status: connections.StatusReady, CheckedAt: &now, CreatedBy: o.Principal.UserID, CreatedAt: now, UpdatedAt: now}
		connection.Details["docker_id"] = health.DockerID
		connection.Details["architecture"] = health.Architecture
		err = s.RuntimeConnections.CreateConnection(ctx, connection)
		if errors.Is(err, connections.ErrExists) {
			existing, getErr := s.RuntimeConnections.GetConnection(ctx, o.OrgID, connectionID)
			if getErr != nil {
				return getErr
			}
			if existing.Cloud != connections.Server || existing.Details["docker_id"] != health.DockerID || existing.Details["host_key"] != o.HostKey {
				return ErrConflict
			}
		} else if err != nil {
			return err
		}
		v.RuntimeConnectionID = connectionID
	}
	v.Health = health
	v.Status = "ready"
	v.UpdatedAt = s.now().UTC()
	if o.Input.Kind == "resize" {
		v.Revision = o.Input.ServerRevision + 1
	}
	return s.Store.SaveServer(ctx, v)
}
func (s *Service) ensureUnused(ctx context.Context, v *Server) error {
	used, err := s.Store.ServerInUse(ctx, v)
	if err != nil {
		return err
	}
	if used {
		return fmt.Errorf("%w: remove or move workloads before changing this server", ErrConflict)
	}
	return s.Store.RetireServer(ctx, v)
}
func (s *Service) removeRuntime(ctx context.Context, o *Operation, v *Server) error {
	if v.RuntimeConnectionID != "" {
		if err := s.RuntimeConnections.DeleteConnection(ctx, o.OrgID, v.RuntimeConnectionID); err != nil && !errors.Is(err, connections.ErrNotFound) {
			return err
		}
		if checkErr := s.Secrets.Delete(ctx, o.OrgID, v.RuntimeConnectionID); checkErr != nil {
			return checkErr
		}
	}
	if v.CreationOperationID != "" {
		return s.Secrets.Delete(ctx, o.OrgID, bootstrapKey(v.CreationOperationID))
	}
	return nil
}
