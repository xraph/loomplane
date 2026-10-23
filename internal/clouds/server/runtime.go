package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	nativemysql "github.com/xraph/loomplane/internal/clouds/mysql"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
)

var ErrOwned = errors.New("the matching Docker resource is not owned by this service")
var ErrMissing = errors.New("the Docker resource is absent")

type Session func(context.Context, map[string]string) (*Client, error)

type Runtime struct {
	Session   Session
	Values    owned.Values
	Ports     owned.PortReservations
	Probe     func(context.Context, *Client, int, string) error
	ProbeRESP func(context.Context, *Client, int) error
	Wait      time.Duration
}

var _ owned.Backend = (*Runtime)(nil)

func ContainerName(iid id.ID) string { return "lp-" + strings.ReplaceAll(iid.String(), "_", "-") }
func runtimeNetwork(labels map[string]string) string {
	env := labels[owned.LabelEnvironment]
	if recovery := labels[LabelRecovery]; recovery != "" {
		env += "/recovery/" + recovery
	}
	return NetworkName(labels[owned.LabelOrg], env)
}

func NetworkName(org, environment string) string {
	sum := sha256.Sum256([]byte(org + "/" + environment))
	return fmt.Sprintf("lp-env-%x", sum[:12])
}

type containerInfo struct {
	ID     string `json:"Id"`
	Image  string
	Mounts []struct{ Type, Name, Destination string }
	Config struct {
		Labels map[string]string
		Image  string
	}
	State struct {
		Running  bool
		Status   string
		ExitCode int
		Error    string
	}
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func (r *Runtime) inspect(ctx context.Context, c *Client, name string, iid id.ID, labels map[string]string) (*containerInfo, error) {
	var out containerInfo
	if checkErr := dockerJSON(ctx, c, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &out); checkErr != nil {
		return nil, checkErr
	}
	if out.Config.Labels[owned.LabelInstance] != iid.String() || out.Config.Labels[owned.LabelOrg] != labels[owned.LabelOrg] || out.Config.Labels[owned.LabelProject] != labels[owned.LabelProject] || out.Config.Labels[owned.LabelEnvironment] != labels[owned.LabelEnvironment] {
		return nil, ErrOwned
	}
	return &out, nil
}

func (r *Runtime) network(ctx context.Context, c *Client, labels map[string]string) (string, error) {
	name := runtimeNetwork(labels)
	var existing struct {
		Internal bool
		ID       string
		Labels   map[string]string
	}
	err := dockerJSON(ctx, c, http.MethodGet, "/networks/"+name, nil, &existing)
	if err == nil {
		if existing.Labels[owned.LabelOrg] != labels[owned.LabelOrg] || existing.Labels[owned.LabelEnvironment] != labels[owned.LabelEnvironment] || existing.Internal != (labels[LabelRecovery] != "") {
			return "", ErrOwned
		}
		return name, nil
	}
	if !errors.Is(err, ErrMissing) {
		return "", err
	}
	var result struct{ ID string }
	if checkErr := dockerJSON(ctx, c, http.MethodPost, "/networks/create", map[string]any{"Name": name, "Driver": "bridge", "Internal": labels[LabelRecovery] != "", "Labels": map[string]string{owned.LabelOrg: labels[owned.LabelOrg], owned.LabelEnvironment: labels[owned.LabelEnvironment]}}, &result); checkErr != nil {
		return "", checkErr
	}
	return name, nil
}

func (r *Runtime) reservation(c *Client, iid id.ID, labels map[string]string) owned.Reservation {
	port := labelPort(labels[owned.LabelHostPort])
	return owned.Reservation{Scope: "docker/" + c.Config.Details["docker_id"], Port: port, Owner: iid.String(), OrgID: labels[owned.LabelOrg], ProjectID: labels[owned.LabelProject], EnvironmentID: labels[owned.LabelEnvironment], ConnectionID: labels[owned.LabelConnection]}
}

func (r *Runtime) reconcilePorts(ctx context.Context, c *Client, iid id.ID, labels map[string]string) error {
	if r.Ports == nil {
		if labels[owned.LabelPublic] == "true" {
			return errors.New("the server has no host port reservation store")
		}
		return nil
	}
	reservation := r.reservation(c, iid, labels)
	if labels[owned.LabelPublic] == "true" {
		if err := r.Ports.Reserve(ctx, reservation); err != nil {
			return err
		}
	} else {
		reservation.Port = 0
	}
	return r.Ports.Reconcile(ctx, reservation)
}

func (r *Runtime) Apply(ctx context.Context, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) (out *cpprovider.ProvisionResult, err error) {
	if checkErr := validateVolumes(spec.Volumes); checkErr != nil {
		return nil, checkErr
	}
	if labels[LabelRecovery] != "" && labels[owned.LabelPublic] == "true" {
		return nil, errors.New("recovery instances cannot publish ports")
	}
	c, err := r.Session(ctx, labels)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	name := ContainerName(iid)
	backup := name + "-previous"
	old, err := r.inspect(ctx, c, name, iid, labels)
	if err != nil && !errors.Is(err, ErrMissing) {
		return nil, err
	}
	previous, previousErr := r.inspect(ctx, c, backup, iid, labels)
	if previousErr != nil && !errors.Is(previousErr, ErrMissing) {
		return nil, previousErr
	}
	stoppedBackup := name + "-previous-stopped"
	stopped, stoppedErr := r.inspect(ctx, c, stoppedBackup, iid, labels)
	if stoppedErr != nil && !errors.Is(stoppedErr, ErrMissing) {
		return nil, stoppedErr
	}
	if previous != nil && stopped != nil {
		return nil, errors.New("multiple previous server containers need reconciliation")
	}
	for _, existing := range []*containerInfo{old, previous, stopped} {
		if existing != nil {
			if checkErr := checkVolumeLayout(existing, iid, spec.Volumes); checkErr != nil {
				return nil, checkErr
			}
		}
	}
	previousRunning := true
	if stopped != nil {
		previous, backup, previousRunning = stopped, stoppedBackup, false
	}
	if old != nil && previous != nil {
		if old.Config.Image == spec.Image && old.Config.Labels[owned.LabelRevision] == labels[owned.LabelRevision] && old.State.Running {
			if checkErr := r.wait(ctx, c, name, iid, labels); checkErr != nil {
				return nil, checkErr
			}
			if checkErr := r.removeContainer(ctx, c, backup, iid, labels); checkErr != nil {
				return nil, checkErr
			}
			if checkErr := r.reconcilePorts(ctx, c, iid, labels); checkErr != nil {
				return nil, checkErr
			}
			return &cpprovider.ProvisionResult{ProviderRef: name, ServiceRefs: map[string]string{spec.Name: old.ID}, Endpoints: r.endpoints(c, iid, spec, labels)}, nil
		}
		if checkErr := r.removeContainer(ctx, c, name, iid, labels); checkErr != nil {
			return nil, checkErr
		}
		old = nil
	}
	if old == nil && previous != nil {
		if checkErr := dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(previous.ID)+"/rename?name="+name, nil, nil); checkErr != nil {
			return nil, checkErr
		}
		if previousRunning {
			if checkErr := dockerSetRunning(ctx, c, previous.ID, true); checkErr != nil {
				return nil, checkErr
			}
		}
		old = previous
		old.State.Running = previousRunning
	}
	mounts, err := ensureVolumes(ctx, c, iid, spec.Volumes, labels)
	if err != nil {
		return nil, err
	}
	if labels[owned.LabelPublic] == "true" && r.Ports == nil {
		return nil, errors.New("the server has no host port reservation store")
	}
	reservation := r.reservation(c, iid, labels)
	reserved, attempted, changed, committed := false, false, false, false
	wasRunning := old != nil && old.State.Running
	if old != nil && !wasRunning {
		backup = stoppedBackup
	}
	defer func() {
		if err == nil || committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		cleaned := true
		if attempted {
			candidate, inspectErr := r.inspect(cleanup, c, name, iid, labels)
			if inspectErr != nil && !errors.Is(inspectErr, ErrMissing) {
				err = errors.Join(err, inspectErr)
				cleaned = false
			}
			if candidate != nil && (old == nil || candidate.ID != old.ID) {
				if candidate.Config.Labels[owned.LabelRevision] != labels[owned.LabelRevision] {
					err = errors.Join(err, ErrOwned)
					cleaned = false
				} else if removeErr := r.removeContainer(cleanup, c, name, iid, labels); removeErr != nil {
					err = errors.Join(err, removeErr)
					cleaned = false
				}
			}
		}
		if changed && old != nil && cleaned {
			current, inspectErr := r.inspect(cleanup, c, name, iid, labels)
			if errors.Is(inspectErr, ErrMissing) {
				err = errors.Join(err, dockerJSON(cleanup, c, http.MethodPost, "/containers/"+url.PathEscape(old.ID)+"/rename?name="+name, nil, nil))
			} else if inspectErr != nil || current.ID != old.ID {
				err = errors.Join(err, inspectErr, ErrOwned)
				return
			}
			if wasRunning {
				err = errors.Join(err, dockerSetRunning(cleanup, c, old.ID, true))
			}
		}
		if reserved && cleaned && (old == nil || old.Config.Labels[owned.LabelHostPort] != strconv.Itoa(reservation.Port)) {
			err = errors.Join(err, r.Ports.Release(cleanup, reservation))
		}
	}()
	if labels[owned.LabelPublic] == "true" {
		if checkErr := r.Ports.Reserve(ctx, reservation); checkErr != nil {
			return nil, checkErr
		}
		reserved = true
		if checkErr := r.checkPort(ctx, c, reservation.Port, old); checkErr != nil {
			return nil, checkErr
		}
	}
	network, err := r.network(ctx, c, labels)
	if err != nil {
		return nil, err
	}
	values := owned.RuntimeValues{}
	if r.Values != nil {
		values, err = r.Values(ctx, labels)
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasPrefix(spec.Image, "sha256:") {
		if checkErr := r.pull(ctx, c, spec.Image, values.PullAuth); checkErr != nil {
			return nil, checkErr
		}
	}
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, values.Env)
	variables := []string{}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		variables = append(variables, key+"="+env[key])
	}
	containerLabels := map[string]string{owned.LabelOrg: labels[owned.LabelOrg], owned.LabelProject: labels[owned.LabelProject], owned.LabelEnvironment: labels[owned.LabelEnvironment], owned.LabelInstance: iid.String(), owned.LabelRevision: labels[owned.LabelRevision], owned.LabelHostPort: labels[owned.LabelHostPort], owned.LabelConnection: labels[owned.LabelConnection]}
	for _, key := range []string{owned.LabelPublic, LabelDatabase, LabelRecovery, nativemysql.LabelDatabase, nativemysql.LabelEngine, nativecache.LabelEngine} {
		if labels[key] != "" {
			containerLabels[key] = labels[key]
		}
	}
	ports := map[string]any{}
	exposed := map[string]any{}
	for _, port := range spec.Ports {
		if port.Container < 1 || port.Container > 65535 {
			return nil, errors.New("invalid server container port")
		}
		key := strconv.Itoa(port.Container) + "/tcp"
		exposed[key] = map[string]any{}
		host, hostPort := "127.0.0.1", ""
		if labels[owned.LabelPublic] == "true" {
			host, hostPort = "0.0.0.0", strconv.Itoa(reservation.Port)
		}
		if labels[LabelRecovery] == "" {
			ports[key] = []map[string]string{{"HostIp": host, "HostPort": hostPort}}
		}
	}
	body := map[string]any{"Image": spec.Image, "Env": variables, "Labels": containerLabels, "ExposedPorts": exposed, "HostConfig": map[string]any{"Init": true, "Mounts": mounts, "RestartPolicy": map[string]string{"Name": "unless-stopped"}, "PortBindings": ports, "Memory": int64(spec.Resources.MemoryMB) * (1 << 20), "NanoCpus": int64(spec.Resources.CPUMillis) * 1000000}, "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{"Aliases": []string{name}}}}}
	if len(spec.Command) > 0 {
		body["Entrypoint"] = spec.Command
	}
	if len(spec.Args) > 0 {
		body["Cmd"] = spec.Args
	}
	if old != nil {
		if old.Config.Image == spec.Image && old.Config.Labels[owned.LabelRevision] == labels[owned.LabelRevision] && old.State.Running {
			if checkErr := r.wait(ctx, c, name, iid, labels); checkErr != nil {
				return nil, checkErr
			}
			if checkErr := r.reconcilePorts(ctx, c, iid, labels); checkErr != nil {
				return nil, checkErr
			}
			return &cpprovider.ProvisionResult{ProviderRef: name, ServiceRefs: map[string]string{spec.Name: old.ID}, Endpoints: r.endpoints(c, iid, spec, labels)}, nil
		}
		changed = true
		if checkErr := dockerSetRunning(ctx, c, old.ID, false); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(old.ID)+"/rename?name="+backup, nil, nil); checkErr != nil {
			return nil, checkErr
		}
	}
	attempted = true
	var result struct {
		ID string `json:"Id"`
	}
	if checkErr := dockerJSON(ctx, c, http.MethodPost, "/containers/create?name="+name, body, &result); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := uploadFiles(ctx, c, result.ID, values); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := dockerSetRunning(ctx, c, result.ID, true); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := r.wait(ctx, c, name, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	committed = true
	if old != nil {
		if checkErr := r.removeContainer(ctx, c, backup, iid, labels); checkErr != nil {
			return nil, checkErr
		}
	}
	if checkErr := r.reconcilePorts(ctx, c, iid, labels); checkErr != nil {
		return nil, checkErr
	}
	return &cpprovider.ProvisionResult{ProviderRef: name, ServiceRefs: map[string]string{spec.Name: result.ID}, Endpoints: r.endpoints(c, iid, spec, labels)}, nil
}

func (r *Runtime) endpoints(c *Client, iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) []cpprovider.Endpoint {
	out := make([]cpprovider.Endpoint, 0, len(spec.Ports))
	if labels[LabelRecovery] != "" {
		return out
	}
	for _, port := range spec.Ports {
		public := labels[owned.LabelPublic] == "true"
		host := ContainerName(iid)
		value := port.Container
		if public {
			host = c.Config.Details["advertise_host"]
			value = labelPort(labels[owned.LabelHostPort])
		}
		protocol := "http"
		if labels[owned.LabelHealthProtocol] == "memcached" && port.Container == labelPort(labels[owned.LabelHealthPort]) {
			protocol = "memcached"
		}
		if labels[LabelDatabase] != "" {
			protocol = "postgresql"
		}
		if labels[nativemysql.LabelDatabase] != "" {
			protocol = "mysql"
		}
		if labels[nativecache.LabelEngine] != "" {
			protocol = "redis"
		}
		out = append(out, cpprovider.Endpoint{ServiceName: spec.Name, URL: protocol + "://" + net.JoinHostPort(host, strconv.Itoa(value)), Port: value, Protocol: protocol, Public: public})
	}
	return out
}

func (r *Runtime) wait(ctx context.Context, c *Client, name string, iid id.ID, labels map[string]string) error {
	wait := r.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		info, err := r.inspect(ctx, c, name, iid, labels)
		if err != nil {
			return err
		}
		if info.State.Running {
			if isDatabase(labels) {
				if probeErr := databaseReady(ctx, c, name, labels); probeErr == nil {
					return nil
				}
			} else if !probed(labels) {
				return nil
			}
			if !isDatabase(labels) {
				probe, probeErr := r.healthProbe(labels)
				if probeErr != nil {
					return probeErr
				}
				for _, value := range healthPorts(info, labels) {
					if err = probe(ctx, c, value); err == nil {
						return nil
					}
				}
			}
		} else if info.State.Status == "exited" || info.State.Status == "dead" {
			return fmt.Errorf("the server container exited before readiness with code %d", info.State.ExitCode)
		}
		if time.Now().After(deadline) {
			return errors.New("the server container did not pass its readiness check")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// healthPorts are the host ports a readiness probe may answer on: the one
// published for LabelHealthPort when the container declares it, otherwise
// every published port.
func healthPorts(info *containerInfo, labels map[string]string) []int {
	var out []int
	for key, ports := range info.NetworkSettings.Ports {
		if declared := labels[owned.LabelHealthPort]; declared != "" && key != declared+"/tcp" {
			continue
		}
		for _, port := range ports {
			if value, err := strconv.Atoi(port.HostPort); err == nil && value > 0 {
				out = append(out, value)
			}
		}
	}
	return out
}

func probeServer(ctx context.Context, c *Client, port int, path string) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return c.ssh.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://service"+path, http.NoBody)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("the server health endpoint returned HTTP %d", res.StatusCode)
}

func (r *Runtime) removeContainer(ctx context.Context, c *Client, name string, iid id.ID, labels map[string]string) error {
	info, err := r.inspect(ctx, c, name, iid, labels)
	if errors.Is(err, ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	return dockerJSON(ctx, c, http.MethodDelete, "/containers/"+url.PathEscape(info.ID)+"?force=1&v=0", nil, nil)
}
func (r *Runtime) Remove(ctx context.Context, iid id.ID, labels map[string]string) error {
	c, err := r.Session(ctx, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	if checkErr := r.removeContainer(ctx, c, ContainerName(iid), iid, labels); checkErr != nil {
		return checkErr
	}
	for _, suffix := range []string{"-previous", "-previous-stopped"} {
		if checkErr := r.removeContainer(ctx, c, ContainerName(iid)+suffix, iid, labels); checkErr != nil {
			return checkErr
		}
	}
	if r.Ports != nil {
		reservation := r.reservation(c, iid, labels)
		reservation.Port = 0
		if checkErr := r.Ports.Reconcile(ctx, reservation); checkErr != nil {
			return checkErr
		}
	}
	network := runtimeNetwork(labels)
	var netInfo struct {
		Labels     map[string]string
		Containers map[string]any
	}
	if err = dockerJSON(ctx, c, http.MethodGet, "/networks/"+network, nil, &netInfo); errors.Is(err, ErrMissing) {
		return nil
	} else if err != nil {
		return err
	}
	if netInfo.Labels[owned.LabelOrg] != labels[owned.LabelOrg] || netInfo.Labels[owned.LabelEnvironment] != labels[owned.LabelEnvironment] {
		return ErrOwned
	}
	if len(netInfo.Containers) == 0 {
		return dockerJSON(ctx, c, http.MethodDelete, "/networks/"+network, nil, nil)
	}
	return nil
}
func (r *Runtime) Status(ctx context.Context, iid id.ID, labels map[string]string) (*cpprovider.InstanceStatus, error) {
	c, err := r.Session(ctx, labels)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if errors.Is(err, ErrMissing) {
		return &cpprovider.InstanceStatus{State: cpprovider.StateStopped, Metadata: map[string]string{"exists": "false"}}, nil
	}
	if err != nil {
		return nil, err
	}
	state, ready := cpprovider.StateStopped, false
	if info.State.Running {
		state, ready = cpprovider.StateRunning, true
		if isDatabase(labels) {
			ready = databaseReady(ctx, c, ContainerName(iid), labels) == nil
			if !ready {
				state = cpprovider.StateStarting
			}
		}
		if probed(labels) {
			ready = false
			probe, probeErr := r.healthProbe(labels)
			for _, value := range healthPorts(info, labels) {
				if probeErr == nil && probe(ctx, c, value) == nil {
					ready = true
				}
			}
			if !ready {
				state = cpprovider.StateStarting
			}
		}
	}
	if info.State.Status == "exited" && info.State.ExitCode != 0 {
		state = cpprovider.StateFailed
	}
	return &cpprovider.InstanceStatus{State: state, Ready: ready, Metadata: map[string]string{"exists": "true", "revision": info.Config.Image}}, nil
}

func (r *Runtime) pull(ctx context.Context, c *Client, image, auth string) error {
	var existing any
	err := dockerJSON(ctx, c, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, &existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrMissing) {
		return err
	}
	response, err := c.RequestHeaders(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(image), nil, http.Header{"X-Registry-Auth": {auth}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("pull selected image returned HTTP %d", response.StatusCode)
	}
	reader := json.NewDecoder(io.LimitReader(response.Body, 32<<20))
	for {
		var frame struct{ Error string }
		if err = reader.Decode(&frame); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if frame.Error != "" {
			if raw, decodeErr := base64.URLEncoding.DecodeString(auth); decodeErr == nil {
				var credential struct {
					Token string `json:"registrytoken"`
				}
				if decodeErr = json.Unmarshal(raw, &credential); decodeErr == nil && credential.Token != "" {
					frame.Error = strings.ReplaceAll(frame.Error, credential.Token, "[redacted]")
				}
			}
			return fmt.Errorf("pull selected image: %s", frame.Error)
		}
	}
}
func (r *Runtime) SetRunning(ctx context.Context, iid id.ID, labels map[string]string, running bool) error {
	c, err := r.Session(ctx, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil {
		return err
	}
	return dockerSetRunning(ctx, c, info.ID, running)
}

func dockerSetRunning(ctx context.Context, c *Client, container string, running bool) error {
	action := "stop?t=10"
	if running {
		action = "start"
	}
	res, err := c.Request(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/"+action, nil)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNotModified {
		// Docker returns 304 when the container already has the requested state.
		defer res.Body.Close()
		return nil
	}
	return dockerResult(res, nil)
}
func (*Runtime) Scale(context.Context, id.ID, map[string]string, cpprovider.ResourceSpec) error {
	return ctrlplane.ErrProviderUnavail
}

func dockerJSON(ctx context.Context, c *Client, method, path string, body, out any) error {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = strings.NewReader(string(data))
	}
	res, err := c.Request(ctx, method, path, input)
	if err != nil {
		return err
	}
	return dockerResult(res, out)
}

// dockerResult reads a Docker response into out, or the error it reports.
func dockerResult(res *http.Response, out any) error {
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return ErrMissing
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var message struct{ Message string }
		if decodeErr := json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&message); decodeErr != nil {
			message.Message = http.StatusText(res.StatusCode)
		}
		return fmt.Errorf("docker returned HTTP %d: %s", res.StatusCode, message.Message)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out)
	}
	return nil
}

func (r *Runtime) checkPort(ctx context.Context, c *Client, port int, old *containerInfo) error {
	var containers []struct {
		ID    string `json:"Id"`
		Ports []struct{ PublicPort int }
	}
	if checkErr := dockerJSON(ctx, c, http.MethodGet, "/containers/json?all=1", nil, &containers); checkErr != nil {
		return checkErr
	}
	for _, container := range containers {
		for _, binding := range container.Ports {
			if binding.PublicPort == port && (old == nil || old.ID != container.ID) {
				return errors.New("the selected host port belongs to another Docker workload")
			}
		}
	}
	return nil
}

func labelPort(raw string) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

// RemovePending verifies the interrupted operation's runtime reference before
// deleting its compute. Volumes remain attached to storage inventory (v=0).
func (r *Runtime) RemovePending(ctx context.Context, iid id.ID, labels map[string]string) error {
	if labels[owned.LabelRuntime] == "" {
		return ErrOwned
	}
	c, err := r.Session(ctx, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	for _, suffix := range []string{"", "-previous", "-previous-stopped"} {
		info, inspectErr := r.inspect(ctx, c, ContainerName(iid)+suffix, iid, labels)
		if errors.Is(inspectErr, ErrMissing) {
			continue
		}
		if inspectErr != nil {
			return inspectErr
		}
		if info.Config.Labels[owned.LabelRuntime] != labels[owned.LabelRuntime] {
			return ErrOwned
		}
	}
	return r.Remove(ctx, iid, labels)
}
