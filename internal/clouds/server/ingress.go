package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const ingressOwner = "loomplane.ingress"

type IngressSnapshot struct {
	ContainerID string          `json:"container_id"`
	ETag        string          `json:"etag"`
	Config      json.RawMessage `json:"config"`
}

func ingressLabels(org, server string) map[string]string {
	return map[string]string{owned.LabelOrg: org, ingressOwner: server}
}

func ingressFence(ctx context.Context, c *Client) error {
	f, ok := EffectFenceFrom(ctx)
	if !ok || !c.RequireFence {
		return errors.New("ingress requires a verified runtime effect fence")
	}
	return c.ArmEffectFence(ctx, f)
}

func ingressContainer(ctx context.Context, c *Client, org, server string) (*containerInfo, error) {
	var out containerInfo
	if err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+IngressName(org, server)+"/json", nil, &out); err != nil {
		return nil, err
	}
	if out.Config.Labels[owned.LabelOrg] != org || out.Config.Labels[ingressOwner] != server || out.Config.Image != IngressImage {
		return nil, ErrOwned
	}
	return &out, nil
}

// EnsureIngress starts the exact owned proxy with durable certificate and
// configuration volumes. Docker refuses occupied public listeners.
func EnsureIngress(ctx context.Context, c *Client, org, server string) (*IngressSnapshot, error) {
	if err := ingressFence(ctx, c); err != nil {
		return nil, err
	}
	name := IngressName(org, server)
	labels := ingressLabels(org, server)
	container, err := ingressContainer(ctx, c, org, server)
	if errors.Is(err, ErrMissing) {
		if checkErr := (&Runtime{}).pull(ctx, c, IngressImage, ""); checkErr != nil {
			return nil, checkErr
		}
		for _, volume := range []string{name + "-data", name + "-config"} {
			var current struct{ Labels map[string]string }
			err = dockerJSON(ctx, c, http.MethodGet, "/volumes/"+volume, nil, &current)
			if errors.Is(err, ErrMissing) {
				err = dockerJSON(ctx, c, http.MethodPost, "/volumes/create", map[string]any{"Name": volume, "Labels": labels}, nil)
			} else if err == nil && (current.Labels[owned.LabelOrg] != org || current.Labels[ingressOwner] != server) {
				err = ErrOwned
			}
			if err != nil {
				return nil, err
			}
		}
		var created struct{ ID string }
		err = dockerJSON(ctx, c, http.MethodPost, "/containers/create?name="+name, map[string]any{
			"Image": IngressImage, "Labels": labels,
			"Cmd":          []string{"caddy", "run", "--resume", "--config", "/config/bootstrap.json"},
			"ExposedPorts": map[string]any{"80/tcp": map[string]any{}, "443/tcp": map[string]any{}},
			"HostConfig": map[string]any{"RestartPolicy": map[string]any{"Name": "unless-stopped"}, "Memory": 256 << 20, "NanoCpus": 500000000,
				"CapDrop": []string{"ALL"}, "CapAdd": []string{"NET_BIND_SERVICE"}, "Sysctls": map[string]string{"net.ipv4.ip_forward": "0"},
				"Binds":        []string{name + "-data:/data", name + "-config:/config"},
				"PortBindings": map[string]any{"80/tcp": []any{map[string]string{"HostIp": "0.0.0.0", "HostPort": "80"}}, "443/tcp": []any{map[string]string{"HostIp": "0.0.0.0", "HostPort": "443"}}}},
		}, &created)
		if err != nil {
			return nil, err
		}
		container, err = ingressContainer(ctx, c, org, server)
	}
	if err != nil {
		return nil, err
	}
	if !container.State.Running {
		bootstrap, configErr := (IngressConfig{OrgID: org, ServerID: server, Version: "initial"}).JSON()
		if configErr != nil {
			return nil, configErr
		}
		if checkErr := ingressFile(ctx, c, container.ID, "bootstrap.json", bootstrap); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := dockerJSON(ctx, c, http.MethodPost, "/containers/"+container.ID+"/start", nil, nil); checkErr != nil {
			return nil, checkErr
		}
	}
	return ReadIngress(ctx, c, org, server)
}

func ingressFile(ctx context.Context, c *Client, container, name string, raw []byte) error {
	var body bytes.Buffer
	w := tar.NewWriter(&body)
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(raw))}); err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	resp, err := c.RequestHeaders(ctx, http.MethodPut, "/containers/"+url.PathEscape(container)+"/archive?path=/config", &body, http.Header{"Content-Type": []string{"application/x-tar"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ingress configuration copy failed: HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// The fixed script only talks to the container's loopback admin. A non-detached
// exec start stays inside the daemon fence until wget exits and readback ends.
const ingressAdminScript = `set -eu
d=$(mktemp -d)
trap 'rm -rf "$d"' EXIT
if [ "$1" = apply ]; then
 printf '%s' "$3" | base64 -d > "$d/new.json"
 wget -q -T 20 --header='Content-Type: application/json' --header="If-Match: $2" --post-file="$d/new.json" -O "$d/result" http://127.0.0.1:2019/config/
fi
wget -S -T 20 -O "$d/config" http://127.0.0.1:2019/config/ 2> "$d/headers"
awk 'tolower($1)=="etag:" {sub(/^[^:]*:[ \t]*/, ""); sub(/\r$/, ""); print}' "$d/headers"
cat "$d/config"
`

func ReadIngress(ctx context.Context, c *Client, org, server string) (*IngressSnapshot, error) {
	container, err := ingressContainer(ctx, c, org, server)
	if err != nil {
		return nil, err
	}
	return ingressAdmin(ctx, c, container.ID, "read", "", nil)
}

func ingressAdmin(ctx context.Context, c *Client, container, action, etag string, config []byte) (*IngressSnapshot, error) {
	var out bytes.Buffer
	err := execIngressOutput(ctx, c, container, []string{"sh", "-c", ingressAdminScript, "ingress", action, etag, base64.StdEncoding.EncodeToString(config)}, &out)
	if err != nil {
		return nil, err
	}
	parts := bytes.SplitN(out.Bytes(), []byte("\n"), 2)
	if len(parts) != 2 || len(parts[0]) == 0 || !json.Valid(parts[1]) {
		return nil, errors.New("ingress admin readback is incomplete")
	}
	return &IngressSnapshot{ContainerID: container, ETag: strings.TrimSpace(string(parts[0])), Config: append([]byte(nil), parts[1]...)}, nil
}

// ApplyIngress preserves the previous config on conflict or invalid input.
// Callers persist expected and desired before this effect and reconcile the
// version in readback after an unknown outcome instead of replaying stale CAS.
func ApplyIngress(ctx context.Context, c *Client, expected *IngressSnapshot, v IngressConfig) (*IngressSnapshot, error) {
	if expected == nil || expected.ETag == "" {
		return nil, ErrInvalid
	}
	if err := ingressFence(ctx, c); err != nil {
		return nil, err
	}
	container, err := ingressContainer(ctx, c, v.OrgID, v.ServerID)
	if err != nil {
		return nil, err
	}
	if container.ID != expected.ContainerID {
		return nil, ErrOwned
	}
	raw, err := v.JSON()
	if err != nil {
		return nil, err
	}
	out, err := ingressAdmin(ctx, c, container.ID, "apply", expected.ETag, raw)
	if err != nil {
		return nil, err
	}
	if !sameIngressJSON(raw, out.Config) {
		return nil, errors.New("ingress config changed during readback")
	}
	return out, nil
}

func sameIngressJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, xerr := json.Marshal(x)
	yb, yerr := json.Marshal(y)
	return xerr == nil && yerr == nil && bytes.Equal(xb, yb)
}

func SameIngressConfig(a, b []byte) bool { return sameIngressJSON(a, b) }

func inspectIngressBackend(ctx context.Context, c *Client, b IngressBackend) error {
	var info containerInfo
	if err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+url.PathEscape(b.ContainerID)+"/json", nil, &info); err != nil {
		return err
	}
	l := info.Config.Labels
	if info.ID != b.ContainerID || !info.State.Running || l[owned.LabelOrg] != b.OrgID || l[owned.LabelProject] != b.ProjectID || l[owned.LabelEnvironment] != b.EnvironmentID || (l[owned.LabelService] != "" && l[owned.LabelService] != b.Service) || l[owned.LabelInstance] != b.InstanceID || l[owned.LabelRevision] != b.ReleaseID {
		return ErrOwned
	}
	if b.Address != "" {
		var network struct {
			Containers map[string]struct{ IPv4Address string }
		}
		if err := dockerJSON(ctx, c, http.MethodGet, "/networks/"+IngressNetwork(b), nil, &network); err != nil {
			return err
		}
		if strings.Split(network.Containers[b.ContainerID].IPv4Address, "/")[0] != b.Address {
			return ErrOwned
		}
	}
	return nil
}

// ConnectIngressBackend attaches only this exact service and ingress to a
// dedicated bridge. Environment networks never become ingress networks.
func ConnectIngressBackend(ctx context.Context, c *Client, org, server string, b IngressBackend) (IngressBackend, error) {
	if err := ingressFence(ctx, c); err != nil {
		return b, err
	}
	if b.OrgID != org || b.Service == "" || b.ProjectID == "" || b.EnvironmentID == "" {
		return b, ErrInvalid
	}
	if err := inspectIngressBackend(ctx, c, b); err != nil {
		return b, err
	}
	proxy, err := ingressContainer(ctx, c, org, server)
	if err != nil {
		return b, err
	}
	name := IngressNetwork(b)
	var network struct {
		ID         string
		Labels     map[string]string
		Containers map[string]struct{ IPv4Address string }
	}
	path := "/networks/" + name
	err = dockerJSON(ctx, c, http.MethodGet, path, nil, &network)
	if errors.Is(err, ErrMissing) {
		labels := ingressLabels(org, server)
		labels[owned.LabelProject], labels[owned.LabelEnvironment], labels["loomplane.service"] = b.ProjectID, b.EnvironmentID, b.Service
		err = dockerJSON(ctx, c, http.MethodPost, "/networks/create", map[string]any{"Name": name, "Driver": "bridge", "Labels": labels, "Options": map[string]string{"com.docker.network.bridge.enable_icc": "true"}}, nil)
		if err == nil {
			err = dockerJSON(ctx, c, http.MethodGet, path, nil, &network)
		}
	}
	if err != nil {
		return b, err
	}
	if network.Labels[owned.LabelOrg] != org || network.Labels[ingressOwner] != server || network.Labels[owned.LabelProject] != b.ProjectID || network.Labels[owned.LabelEnvironment] != b.EnvironmentID || network.Labels["loomplane.service"] != b.Service {
		return b, ErrOwned
	}
	for id := range network.Containers {
		if id != b.ContainerID && id != proxy.ID {
			return b, ErrOwned
		}
	}
	for _, id := range []string{b.ContainerID, proxy.ID} {
		if _, ok := network.Containers[id]; !ok {
			if checkErr := dockerJSON(ctx, c, http.MethodPost, path+"/connect", map[string]any{"Container": id}, nil); checkErr != nil {
				return b, checkErr
			}
		}
	}
	if checkErr := dockerJSON(ctx, c, http.MethodGet, path, nil, &network); checkErr != nil {
		return b, checkErr
	}
	b.Address = strings.Split(network.Containers[b.ContainerID].IPv4Address, "/")[0]
	if b.Address == "" {
		return b, errors.New("routed container has no private address")
	}
	return b, nil
}

const ingressProbeScript = `wget -q -S -T 20 --header="Host: $1" -O /dev/null http://127.0.0.1:8081/ 2>&1 || true`

func ProbeIngressBackend(ctx context.Context, c *Client, v IngressConfig, expected *IngressSnapshot, routeID string) error {
	return probeIngress(ctx, c, v, expected, routeID, false)
}
func ProbeIngressPreparation(ctx context.Context, c *Client, v IngressConfig, expected *IngressSnapshot, routeID string) error {
	return probeIngress(ctx, c, v, expected, routeID, true)
}
func probeIngress(ctx context.Context, c *Client, v IngressConfig, expected *IngressSnapshot, routeID string, preparation bool) error {
	var route *IngressRoute
	for i := range v.Routes {
		if v.Routes[i].ID == routeID {
			route = &v.Routes[i]
		}
	}
	if route == nil || !validIngressHost(route.Hostname) || expected == nil {
		return ErrInvalid
	}
	backend := route.Backend
	if route.PreparedBackend != nil {
		if !preparation {
			return ErrOwned
		}
		backend = *route.PreparedBackend
	}
	raw, err := v.JSON()
	if err != nil || !sameIngressJSON(raw, expected.Config) {
		return ErrOwned
	}
	if checkErr := inspectIngressBackend(ctx, c, backend); checkErr != nil {
		return checkErr
	}
	before, err := ReadIngress(ctx, c, v.OrgID, v.ServerID)
	if err != nil {
		return err
	}
	if before.ContainerID != expected.ContainerID || before.ETag != expected.ETag || !sameIngressJSON(before.Config, expected.Config) {
		return ErrOwned
	}
	var out bytes.Buffer
	err = execIngressOutput(ctx, c, before.ContainerID, []string{"sh", "-c", ingressProbeScript, "probe", route.Hostname}, &out)
	if err != nil {
		return err
	}
	var fields []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "HTTP/") {
			fields = strings.Fields(line)
			break
		}
	}
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return errors.New("backend did not return an HTTP response")
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil || code < 200 || code >= 400 {
		return fmt.Errorf("backend request did not succeed: HTTP %d", code)
	}
	after, err := ReadIngress(ctx, c, v.OrgID, v.ServerID)
	if err != nil {
		return err
	}
	if after.ContainerID != before.ContainerID || after.ETag != before.ETag {
		return ErrOwned
	}
	return inspectIngressBackend(ctx, c, backend)
}

// DisconnectIngressBackend removes a retired service bridge after the complete
// config no longer references it. Certificate volumes remain available for reuse.
func DisconnectIngressBackend(ctx context.Context, c *Client, v IngressConfig, b IngressBackend) error {
	if err := ingressFence(ctx, c); err != nil {
		return err
	}
	actual, err := ReadIngress(ctx, c, v.OrgID, v.ServerID)
	if err != nil {
		return err
	}
	raw, err := v.JSON()
	if err != nil || !SameIngressConfig(raw, actual.Config) {
		return ErrOwned
	}

	for _, route := range v.Routes {
		if IngressNetwork(route.Backend) == IngressNetwork(b) || (route.PreparedBackend != nil && IngressNetwork(*route.PreparedBackend) == IngressNetwork(b)) {
			return nil
		}
	}
	proxy, err := ingressContainer(ctx, c, v.OrgID, v.ServerID)
	if err != nil {
		return err
	}
	var network struct {
		Labels     map[string]string
		Containers map[string]json.RawMessage
	}
	path := "/networks/" + IngressNetwork(b)
	err = dockerJSON(ctx, c, http.MethodGet, path, nil, &network)
	if errors.Is(err, ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if network.Labels[owned.LabelOrg] != v.OrgID || network.Labels[ingressOwner] != v.ServerID || network.Labels[owned.LabelProject] != b.ProjectID || network.Labels[owned.LabelEnvironment] != b.EnvironmentID || network.Labels[owned.LabelService] != b.Service {
		return ErrOwned
	}
	for id := range network.Containers {
		if id != proxy.ID && id != b.ContainerID {
			return ErrOwned
		}
	}
	for id := range network.Containers {
		if checkErr := dockerJSON(ctx, c, http.MethodPost, path+"/disconnect", map[string]any{"Container": id, "Force": false}, nil); checkErr != nil {
			return checkErr
		}
	}
	return dockerJSON(ctx, c, http.MethodDelete, path, nil, nil)
}
