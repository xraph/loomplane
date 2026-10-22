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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	return ingressContainerNamed(ctx, c, org, server, IngressName(org, server))
}

func ingressContainerNamed(ctx context.Context, c *Client, org, server, name string) (*containerInfo, error) {
	var out containerInfo
	if err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+name+"/json", nil, &out); err != nil {
		return nil, err
	}
	if out.Config.Labels[owned.LabelOrg] != org || out.Config.Labels[ingressOwner] != server || !slices.Contains(ingressImages(), out.Config.Image) {
		return nil, ErrOwned
	}
	return &out, nil
}

// createIngress makes the proxy container on its durable volumes and the
// public ports. Docker refuses occupied listeners when it starts.
func createIngress(ctx context.Context, c *Client, org, server, image string) error {
	name := IngressName(org, server)
	return dockerJSON(ctx, c, http.MethodPost, "/containers/create?name="+name, map[string]any{
		"Image": image, "Labels": ingressLabels(org, server),
		"Cmd":          []string{"caddy", "run", "--resume", "--config", "/config/bootstrap.json"},
		"ExposedPorts": map[string]any{"80/tcp": map[string]any{}, "443/tcp": map[string]any{}},
		"HostConfig": map[string]any{"RestartPolicy": map[string]any{"Name": "unless-stopped"}, "Memory": 256 << 20, "NanoCpus": 500000000,
			"CapDrop": []string{"ALL"}, "CapAdd": []string{"NET_BIND_SERVICE"}, "Sysctls": map[string]string{"net.ipv4.ip_forward": "0"},
			"Binds":        []string{name + "-data:/data", name + "-config:/config"},
			"PortBindings": map[string]any{"80/tcp": []any{map[string]string{"HostIp": "0.0.0.0", "HostPort": "80"}}, "443/tcp": []any{map[string]string{"HostIp": "0.0.0.0", "HostPort": "443"}}}},
	}, nil)
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
		image := ingressCreateImage()
		if checkErr := (&Runtime{}).pull(ctx, c, image, ""); checkErr != nil {
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
		if err = createIngress(ctx, c, org, server, image); err != nil {
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
	return ingressArchive(ctx, c, container, &body)
}

// ingressArchive unpacks a tar into the proxy's /config volume.
func ingressArchive(ctx context.Context, c *Client, container string, body io.Reader) error {
	resp, err := c.RequestHeaders(ctx, http.MethodPut, "/containers/"+url.PathEscape(container)+"/archive?path=/config", body, http.Header{"Content-Type": []string{"application/x-tar"}})
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
if [ "$1" = reload ]; then
 printf '%s' "$3" | base64 -d > "$d/new.json"
 wget -q -T 20 --header='Content-Type: application/json' --header='Cache-Control: must-revalidate' --header="If-Match: $2" --post-file="$d/new.json" -O "$d/result" http://127.0.0.1:2019/config/
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

// ReloadIngress has Caddy load its unchanged config again, which reads a
// rewritten DNS token file. The config and its ETag stay as they were.
func ReloadIngress(ctx context.Context, c *Client, org, server string, expected *IngressSnapshot) (*IngressSnapshot, error) {
	if expected == nil || expected.ETag == "" {
		return nil, ErrInvalid
	}
	if err := ingressFence(ctx, c); err != nil {
		return nil, err
	}
	container, err := ingressContainer(ctx, c, org, server)
	if err != nil {
		return nil, err
	}
	if container.ID != expected.ContainerID {
		return nil, ErrOwned
	}
	out, err := ingressAdmin(ctx, c, container.ID, "reload", expected.ETag, expected.Config)
	if err != nil {
		return nil, err
	}
	if !sameIngressJSON(expected.Config, out.Config) {
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
	if route == nil || !validIngressRoute(*route) || expected == nil {
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
	err = execIngressOutput(ctx, c, before.ContainerID, []string{"sh", "-c", ingressProbeScript, "probe", IngressProbeHostname(route.ID, route.Hostname)}, &out)
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

// IngressNeedsUpgrade reports whether the server's proxy can't answer DNS-01
// challenges. A server without a proxy needs none once the DNS-capable image
// is published, since EnsureIngress starts that image.
func IngressNeedsUpgrade(ctx context.Context, c *Client, org, server string) (bool, error) {
	container, err := ingressContainer(ctx, c, org, server)
	if errors.Is(err, ErrMissing) {
		if !IngressImagePinned() {
			return true, nil
		}
		// An upgrade that stopped halfway leaves only the stopped proxy.
		_, err = ingressContainerNamed(ctx, c, org, server, previousIngressName(org, server))
		if errors.Is(err, ErrMissing) {
			return false, nil
		}
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if container.Config.Image != dnsIngressImage {
		return true, nil
	}
	// An upgrade that stopped before removing the old proxy, or before
	// starting the new one, left the old one under its previous name.
	_, err = ingressContainerNamed(ctx, c, org, server, previousIngressName(org, server))
	if errors.Is(err, ErrMissing) {
		return false, nil
	}
	return err == nil, err
}

func previousIngressName(org, server string) string { return IngressName(org, server) + "-previous" }

// UpgradeIngress moves the proxy to the DNS-capable image: it stops and
// renames the old container, starts the new one on the same volumes, ports
// and route networks with --resume, and checks Caddy came back with current.
// Any failure restarts the old proxy. Every HTTPS hostname on the server is
// down while the new proxy starts.
func UpgradeIngress(ctx context.Context, c *Client, org, server string, current *IngressSnapshot) (*IngressSnapshot, error) {
	if !IngressImagePinned() || current == nil || current.ETag == "" {
		return nil, ErrInvalid
	}
	if err := ingressFence(ctx, c); err != nil {
		return nil, err
	}
	image, previous := dnsIngressImage, previousIngressName(org, server)
	proxy, err := ingressContainer(ctx, c, org, server)
	if err == nil && proxy.Config.Image == image {
		if proxy.State.Running {
			// An earlier attempt finished; only its stopped proxy can be left.
			return finishIngressUpgrade(ctx, c, org, server, current)
		}
		// An earlier attempt made the new proxy and never started it. The
		// old one waits stopped under its previous name.
		if _, err = ingressContainerNamed(ctx, c, org, server, previous); err != nil {
			return nil, err
		}
		if err = dockerJSON(ctx, c, http.MethodDelete, "/containers/"+url.PathEscape(proxy.ID), nil, nil); err != nil {
			return nil, err
		}
		err = ErrMissing
	}
	var old *containerInfo
	renamed := false
	switch {
	case err == nil:
		old = proxy
		// A stop that failed or was cut off leaves the old proxy stopped.
		// The new one must still resume current, which waitIngressConfig checks.
		if old.State.Running {
			actual, readErr := ingressAdmin(ctx, c, old.ID, "read", "", nil)
			if readErr != nil {
				return nil, readErr
			}
			if !sameIngressJSON(actual.Config, current.Config) {
				return nil, ErrOwned
			}
		}
	case errors.Is(err, ErrMissing):
		if old, err = ingressContainerNamed(ctx, c, org, server, previous); err != nil {
			return nil, err
		}
		renamed = true
	default:
		return nil, err
	}
	networks, err := ingressRouteNetworks(ctx, c, old.ID)
	if err != nil {
		return nil, err
	}
	if err = (&Runtime{}).pull(ctx, c, image, ""); err != nil {
		return nil, err
	}
	if old.State.Running {
		err = dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(old.ID)+"/stop?t=10", nil, nil)
		if err == nil {
			err = upgradeStep("stop")
		}
		// Docker may stop the proxy even when the answer is lost.
		if err != nil {
			return nil, errors.Join(err, restoreIngress(ctx, c, org, server, old.ID, ""))
		}
	}
	if !renamed {
		if err = dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(old.ID)+"/rename?name="+previous, nil, nil); err != nil {
			return nil, errors.Join(err, restoreIngress(ctx, c, org, server, old.ID, ""))
		}
	}
	snapshot, err := startUpgradedIngress(ctx, c, org, server, image, networks, current)
	if err != nil {
		var created string
		if proxy, lookupErr := ingressContainer(ctx, c, org, server); lookupErr == nil && proxy.ID != old.ID {
			created = proxy.ID
		}
		return nil, errors.Join(err, restoreIngress(ctx, c, org, server, old.ID, created))
	}
	// The new proxy serves from here; a retry removes an old one left behind.
	if err = upgradeStep("started"); err != nil {
		return nil, err
	}
	if err = dockerJSON(ctx, c, http.MethodDelete, "/containers/"+url.PathEscape(old.ID), nil, nil); err != nil && !errors.Is(err, ErrMissing) {
		return nil, err
	}
	return snapshot, nil
}

// upgradeFault lets tests fail UpgradeIngress after a step, as a lost
// Docker answer or a crash would. It is nil outside tests.
var upgradeFault func(step string) error

func upgradeStep(step string) error {
	if upgradeFault == nil {
		return nil
	}
	return upgradeFault(step)
}

// ingressRouteNetworks are the route bridges a proxy is attached to.
func ingressRouteNetworks(ctx context.Context, c *Client, container string) ([]string, error) {
	var info struct {
		NetworkSettings struct{ Networks map[string]json.RawMessage }
	}
	if err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+url.PathEscape(container)+"/json", nil, &info); err != nil {
		return nil, err
	}
	out := []string{}
	for network := range info.NetworkSettings.Networks {
		if strings.HasPrefix(network, "lp-route-") {
			out = append(out, network)
		}
	}
	slices.Sort(out)
	return out, nil
}

func startUpgradedIngress(ctx context.Context, c *Client, org, server, image string, networks []string, current *IngressSnapshot) (*IngressSnapshot, error) {
	if err := createIngress(ctx, c, org, server, image); err != nil {
		return nil, err
	}
	proxy, err := ingressContainer(ctx, c, org, server)
	if err != nil {
		return nil, err
	}
	if proxy.Config.Image != image {
		return nil, ErrOwned
	}
	for _, network := range networks {
		if err = dockerJSON(ctx, c, http.MethodPost, "/networks/"+network+"/connect", map[string]any{"Container": proxy.ID}, nil); err != nil {
			return nil, err
		}
	}
	if err = dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(proxy.ID)+"/start", nil, nil); err != nil {
		return nil, err
	}
	return waitIngressConfig(ctx, c, proxy.ID, current)
}

// waitIngressConfig reads the admin config once Caddy listens, and accepts
// only the config the old proxy served.
func waitIngressConfig(ctx context.Context, c *Client, container string, current *IngressSnapshot) (*IngressSnapshot, error) {
	var err error
	for range 20 {
		var snapshot *IngressSnapshot
		if snapshot, err = ingressAdmin(ctx, c, container, "read", "", nil); err == nil {
			if !sameIngressJSON(snapshot.Config, current.Config) {
				return nil, errors.New("upgraded ingress resumed a different config")
			}
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, err
}

// restoreIngress removes a new proxy that failed and brings the old one back
// under its own name.
func restoreIngress(ctx context.Context, c *Client, org, server, old, created string) error {
	// The restore runs even when the caller gave up; the fence stays on ctx.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if created != "" {
		if err := dockerJSON(ctx, c, http.MethodDelete, "/containers/"+url.PathEscape(created)+"?force=true", nil, nil); err != nil && !errors.Is(err, ErrMissing) {
			return err
		}
	}
	if err := dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(old)+"/rename?name="+IngressName(org, server), nil, nil); err != nil {
		// A rename that failed before the move leaves the old name in place.
		var info struct{ Name string }
		if inspectErr := dockerJSON(ctx, c, http.MethodGet, "/containers/"+url.PathEscape(old)+"/json", nil, &info); inspectErr != nil || strings.TrimPrefix(info.Name, "/") != IngressName(org, server) {
			return err
		}
	}
	return dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(old)+"/start", nil, nil)
}

// finishIngressUpgrade checks a proxy an earlier attempt already upgraded
// and removes the stopped one it replaced.
func finishIngressUpgrade(ctx context.Context, c *Client, org, server string, current *IngressSnapshot) (*IngressSnapshot, error) {
	snapshot, err := ReadIngress(ctx, c, org, server)
	if err != nil {
		return nil, err
	}
	if !sameIngressJSON(snapshot.Config, current.Config) {
		return nil, ErrOwned
	}
	old, err := ingressContainerNamed(ctx, c, org, server, previousIngressName(org, server))
	if errors.Is(err, ErrMissing) {
		return snapshot, nil
	}
	if err != nil {
		return nil, err
	}
	if old.State.Running {
		return nil, ErrOwned
	}
	if err = dockerJSON(ctx, c, http.MethodDelete, "/containers/"+url.PathEscape(old.ID), nil, nil); err != nil && !errors.Is(err, ErrMissing) {
		return nil, err
	}
	return snapshot, nil
}

// Cloudflare API tokens, as the Caddy module checks them. A token it
// refuses would appear in Caddy's error, so Loomplane refuses it first.
var ingressDNSToken = regexp.MustCompile(`^([A-Za-z0-9_-]{35,50}|cf(ut|at)_[A-Za-z0-9_-]{32,256})$`)

// WriteIngressDNSToken puts a zone's DNS challenge token in the proxy's
// config volume, readable only by Caddy. Caddy's config names the file;
// the token never enters a config, a readback or a log.
func WriteIngressDNSToken(ctx context.Context, c *Client, org, server, zoneID string, token []byte) error {
	if !ingressZoneID.MatchString(zoneID) || !ingressDNSToken.Match(token) {
		return ErrInvalid
	}
	if err := ingressFence(ctx, c); err != nil {
		return err
	}
	container, err := ingressContainer(ctx, c, org, server)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	w := tar.NewWriter(&body)
	if err = w.WriteHeader(&tar.Header{Name: "dns/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		return err
	}
	if err = w.WriteHeader(&tar.Header{Name: "dns/" + zoneID + ".token", Mode: 0o400, Size: int64(len(token))}); err != nil {
		return err
	}
	if _, err = w.Write(token); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return ingressArchive(ctx, c, container.ID, &body)
}

// The fixed script removes one token file; the fence checks the zone ID.
const ingressTokenScript = `rm -f /config/dns/"$1".token`

// RemoveIngressDNSToken deletes a zone's token once no wildcard route on
// the server needs it.
func RemoveIngressDNSToken(ctx context.Context, c *Client, org, server, zoneID string) error {
	if !ingressZoneID.MatchString(zoneID) {
		return ErrInvalid
	}
	if err := ingressFence(ctx, c); err != nil {
		return err
	}
	container, err := ingressContainer(ctx, c, org, server)
	if err != nil {
		return err
	}
	return execIngressOutput(ctx, c, container.ID, []string{"sh", "-c", ingressTokenScript, "dns", zoneID}, io.Discard)
}
