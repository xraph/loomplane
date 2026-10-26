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
	"maps"
	"net"
	"net/url"
	"slices"
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
	effectiveCPU, effectiveMemory, capacityErr := client.EffectiveCapacity(ctx, cpus, memory)
	if capacityErr != nil {
		effectiveCPU, effectiveMemory = 0, 0
	}
	var storageBytes, freeStorageBytes *int64
	if storage, e := client.StorageCapacity(ctx); e == nil {
		storageBytes = &storage.TotalBytes
		freeStorageBytes = &storage.FreeBytes
	}
	health := RuntimeHealth{StorageBytes: storageBytes, FreeStorageBytes: freeStorageBytes, EffectiveCPUMillis: effectiveCPU, EffectiveMemoryBytes: effectiveMemory, DockerID: details["docker_id"], Architecture: details["architecture"], CPUs: cpus, MemoryBytes: memory, CheckedAt: time.Now().UTC()}
	// GPUs reports a failed read as status unknown, so a GPU-less host never fails inspection.
	if inv, gpuErr := client.GPUs(ctx); gpuErr == nil {
		health.GPUs, health.GPURuntime, health.GPUMessage = inv.Devices, inv.Status, inv.Message
	}
	return health, nil
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
	packages := []string{"docker.io", "python3", "iproute2"}
	runcmd := []any{[]string{"systemctl", "enable", "--now", "docker"}, []string{"python3", "/root/loomplane-enroll.py"}}
	if nvidiaAccelerator(o.Accelerator) {
		// The GPU-ready image ships the driver and may ship Docker, so the
		// script installs Docker only when it's missing, adds the NVIDIA
		// Container Toolkit when that's missing, and Docker is pointed at it
		// before the server enrolls. A failure here still enrolls the server,
		// and its GPU check then says why it can't take GPU work.
		packages = []string{"python3", "iproute2", "ca-certificates", "curl", "gnupg"}
		runcmd = []any{[]string{"bash", "-c", nvidiaToolkitScript}, []string{"systemctl", "enable", "--now", "docker"}, []string{"nvidia-ctk", "runtime", "configure", "--runtime=docker"}, []string{"systemctl", "restart", "docker"}, []string{"python3", "/root/loomplane-enroll.py"}}
	}
	document := map[string]any{"package_update": true, "groups": []string{"docker"}, "packages": packages, "users": []any{"default", map[string]any{"name": "loomplane", "groups": []string{"docker"}, "shell": "/bin/bash", "ssh_authorized_keys": []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}}}, "write_files": []any{map[string]any{"path": "/root/loomplane-enroll.py", "permissions": "0600", "content": script}}, "runcmd": runcmd}
	encoded, err := json.Marshal(document)
	return "#cloud-config\n" + string(encoded), err
}

// nvidiaToolkitScript prepares a GPU-ready image for containers. It installs
// Docker only when the image has none, so an engine the image ships stays,
// then installs the NVIDIA Container Toolkit from NVIDIA's apt repository
// unless nvidia-ctk is already there. It never installs a driver. Any failed
// step stops the script, and the server's GPU check then says what's missing.
const nvidiaToolkitScript = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker >/dev/null 2>&1; then
  apt-get -o DPkg::Lock::Timeout=300 update
  apt-get -o DPkg::Lock::Timeout=300 install -y docker.io
fi
if command -v nvidia-ctk >/dev/null 2>&1; then exit 0; fi
keyring=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
download=$(mktemp -d)
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey -o "$download/gpgkey"
gpg --batch --yes --dearmor -o "$keyring" "$download/gpgkey"
curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list -o "$download/list"
sed "s#deb https://#deb [signed-by=$keyring] https://#g" "$download/list" > /etc/apt/sources.list.d/nvidia-container-toolkit.list
apt-get -o DPkg::Lock::Timeout=300 update
apt-get -o DPkg::Lock::Timeout=300 install -y nvidia-container-toolkit
`

func nvidiaAccelerator(a *provider.Accelerator) bool {
	return a != nil && a.Vendor == provider.VendorNVIDIA
}

// gpuHealth keeps an NVIDIA server's GPU state honest. A probe that found no
// GPU, or couldn't run, on a server bought with GPUs leaves the count unknown
// rather than zero, and a server that can't take GPU work always says why.
func gpuHealth(h RuntimeHealth, a *provider.Accelerator) RuntimeHealth {
	if !nvidiaAccelerator(a) || h.GPURuntime == provider.GPUStatusReady {
		return h
	}
	if h.GPURuntime == "" || h.GPURuntime == provider.GPUStatusNone {
		h.GPURuntime = provider.GPUStatusUnknown
	}
	if h.GPUMessage == "" {
		title := a.Kind
		if k, ok := provider.LookupAcceleratorKind(a.Kind); ok {
			title = k.Title
		}
		noun, them := "GPU", "it"
		if a.Count != 1 {
			noun, them = "GPUs", "them"
		}
		h.GPUMessage = fmt.Sprintf("This server was bought with %d %s %s, but Loomplane can't confirm Docker can use %s, so it takes no GPU work and still costs money. Nothing retries the setup by itself. Install the NVIDIA Container Toolkit on the server, run nvidia-ctk runtime configure --runtime=docker, restart Docker and check the server again, or delete the server.", a.Count, title, noun, them)
	}
	return h
}

// runtimeGPUDetails is the GPU inventory a runtime connection carries for
// planning, or nil when the health check couldn't say anything about GPUs.
func runtimeGPUDetails(h RuntimeHealth) map[string]string {
	if h.GPURuntime == "" {
		return nil
	}
	return provider.GPUInventory{Status: h.GPURuntime, Message: h.GPUMessage, Devices: h.GPUs, CheckedAt: h.CheckedAt}.Details()
}

// syncRuntimeGPUs writes the latest GPU inventory into a server's runtime
// connection. It writes only when the status, message or devices changed, so
// a routine check doesn't rewrite the connection.
func (s *Service) syncRuntimeGPUs(ctx context.Context, conn *connections.Connection, h RuntimeHealth) error {
	details := runtimeGPUDetails(h)
	if details == nil || conn == nil {
		return nil
	}
	if old, ok := provider.ParseGPUInventory(conn.Details); ok && conn.Details[provider.GPUDetailStatus] == h.GPURuntime && old.Status == h.GPURuntime && old.Message == h.GPUMessage && slices.Equal(old.Devices, h.GPUs) {
		return nil
	}
	updated := *conn
	updated.Details = maps.Clone(conn.Details)
	if updated.Details == nil {
		updated.Details = map[string]string{}
	}
	maps.Copy(updated.Details, details)
	return s.RuntimeConnections.UpdateConnection(ctx, &updated)
}

// checkedRuntimeGPUs is syncRuntimeGPUs for a routine server check. A
// connection changed under the check is read again and written once more; if
// it changes again the check carries on, and the next one writes the inventory.
func (s *Service) checkedRuntimeGPUs(ctx context.Context, org string, conn *connections.Connection, h RuntimeHealth) error {
	err := s.syncRuntimeGPUs(ctx, conn, h)
	if !errors.Is(err, connections.ErrChanged) {
		return err
	}
	fresh, getErr := s.RuntimeConnections.GetConnection(ctx, org, conn.ID)
	if getErr != nil {
		return getErr
	}
	if err = s.syncRuntimeGPUs(ctx, fresh, h); errors.Is(err, connections.ErrChanged) {
		s.warn("Couldn't write a server's GPU inventory to its runtime connection; the next check tries again", err)
		return nil
	}
	return err
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
	var conn *connections.Connection
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
		current, key, err := s.Connections.Credential(ctx, o.OrgID, v.RuntimeConnectionID)
		if err != nil {
			return err
		}
		conn, raw, settings = current, key, current.Details
	}
	health, err := s.Runtime.Inspect(ctx, raw, settings, false)
	if err != nil {
		return ErrPending
	}
	if v.Health.DockerID != "" && health.DockerID != v.Health.DockerID {
		return provider.ErrInfrastructureIdentity
	}
	// A GPU server enrolls even when Docker can't use its GPUs yet: it exists
	// and costs money. Its health says why, and it takes no GPU work.
	health = gpuHealth(health, o.Accelerator)
	if o.Input.Kind == "create" {
		config, configErr := server.ParseConfig(raw, settings)
		if configErr != nil {
			return configErr
		}
		settings = config.RecordedDetails(map[string]string{"docker_id": health.DockerID, "architecture": health.Architecture})
		// Stable connection ID allows recovery after a lost database response.
		connectionID := "conn_" + strings.TrimPrefix(o.ServerID, "server_")
		if checkErr := s.Secrets.Put(ctx, o.OrgID, connectionID, raw); checkErr != nil {
			return checkErr
		}
		now := s.now().UTC()
		connection := &connections.Connection{ID: connectionID, OrgID: o.OrgID, Cloud: connections.Server, Name: "Server " + o.ServerID, Account: health.DockerID, Details: settings, Status: connections.StatusReady, CheckedAt: &now, CreatedBy: o.Principal.UserID, CreatedAt: now, UpdatedAt: now}
		// Planning reads GPUs from the runtime connection, so it carries them from the start.
		maps.Copy(connection.Details, runtimeGPUDetails(health))
		err = s.RuntimeConnections.CreateConnection(ctx, connection)
		if errors.Is(err, connections.ErrExists) {
			existing, getErr := s.RuntimeConnections.GetConnection(ctx, o.OrgID, connectionID)
			if getErr != nil {
				return getErr
			}
			if existing.Cloud != connections.Server || existing.Details["docker_id"] != health.DockerID || existing.Details["host_key"] != o.HostKey {
				return ErrConflict
			}
			if syncErr := s.syncRuntimeGPUs(ctx, existing, health); syncErr != nil {
				return syncErr
			}
		} else if err != nil {
			return err
		}
		v.RuntimeConnectionID = connectionID
	} else if conn != nil {
		if syncErr := s.syncRuntimeGPUs(ctx, conn, health); syncErr != nil {
			return syncErr
		}
	}
	if o.Input.Kind == "create" || o.Input.Kind == "resize" {
		priceServer(v, o)
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
