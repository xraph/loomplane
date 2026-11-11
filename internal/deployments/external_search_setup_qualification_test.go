//go:build qualification

package deployments

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/recipes"
)

const externalSearchSetupLabel = "loomplane.qualification.search"

type externalSearchDocker func(context.Context, ...string) ([]byte, error)

type externalSearchObject struct {
	Kind     string `json:"-"`
	ID       string
	Name     string
	Scope    string
	Created  string
	Internal bool
	Driver   string
}

func inspectExternalSearchObject(ctx context.Context, docker externalSearchDocker, kind, selector string) (externalSearchObject, error) {
	var format string
	switch kind {
	case "container":
		format = `{"ID":{{json .Id}},"Name":{{json .Name}},"Scope":{{json (index .Config.Labels "loomplane.qualification.search")}},"Created":{{json .Created}}}`
	case "network":
		format = `{"ID":{{json .Id}},"Name":{{json .Name}},"Scope":{{json (index .Labels "loomplane.qualification.search")}},"Created":{{json .Created}},"Internal":{{json .Internal}},"Driver":{{json .Driver}}}`
	case "volume":
		format = `{"ID":{{json .Name}},"Name":{{json .Name}},"Scope":{{json (index .Labels "loomplane.qualification.search")}},"Created":{{json .CreatedAt}}}`
	default:
		return externalSearchObject{}, errors.New("unknown search qualification object")
	}
	raw, err := docker(ctx, kind, "inspect", "--format", format, selector)
	var object externalSearchObject
	if err != nil || json.Unmarshal(raw, &object) != nil || object.ID == "" || object.Created == "" {
		return externalSearchObject{}, fmt.Errorf("could not inspect qualification %s", kind)
	}
	object.Kind = kind
	object.Name = strings.TrimPrefix(object.Name, "/")
	return object, nil
}

type externalSearchSnapshot map[string][]string

func captureExternalSearchObjects(ctx context.Context, docker externalSearchDocker, label string) (externalSearchSnapshot, error) {
	objects := externalSearchSnapshot{}
	for _, kind := range []string{"container", "volume", "network"} {
		args := []string{kind, "ls", "--quiet"}
		if kind == "container" {
			args = append(args, "--all", "--no-trunc")
		} else if kind == "network" {
			args = append(args, "--no-trunc")
		}
		if label != "" {
			args = append(args, "--filter", "label="+label)
		}
		raw, err := docker(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("could not list qualification %s identities", kind)
		}
		objects[kind] = strings.Fields(string(raw))
	}
	return objects, nil
}

func ownedExternalSearchObject(actual, expected externalSearchObject, baseline externalSearchSnapshot) bool {
	return actual.Kind == expected.Kind && actual.ID == expected.ID && actual.Name == expected.Name &&
		actual.Scope == expected.Scope && expected.Scope != "" && actual.Created != "" &&
		(expected.Created == "" || actual.Created == expected.Created) &&
		(actual.Kind != "network" || actual.Driver == "bridge" && !actual.Internal) && !slices.Contains(baseline[actual.Kind], actual.ID)
}

func removeExternalSearchObject(ctx context.Context, docker externalSearchDocker, expected externalSearchObject, baseline externalSearchSnapshot) error {
	actual, err := inspectExternalSearchObject(ctx, docker, expected.Kind, expected.ID)
	if err != nil || expected.Created == "" || !ownedExternalSearchObject(actual, expected, baseline) {
		return fmt.Errorf("qualification %s changed identity before cleanup", expected.Kind)
	}
	args := []string{expected.Kind, "rm"}
	if expected.Kind == "container" {
		args = append(args, "--force")
	}
	if _, err := docker(ctx, append(args, expected.ID)...); err != nil {
		return fmt.Errorf("qualification %s could not be removed", expected.Kind)
	}
	return nil
}

func externalSearchRuntimeFiles(t *testing.T, ctx context.Context, kind recipes.Kind, registry *recipes.Registry, comp *recipes.Composition, architecture string, iid id.ID, directory string) (catalogconnect.EngineAccess, []string) {
	t.Helper()
	f := newComponentFixture(t, registry)
	job := f.graphJobForEngine(t, kind.Name, comp.Version, comp)
	job.Action.Resource.TargetSettings["architecture"] = architecture
	run, _, err := f.target.componentEnv(ctx, job, kind)
	if err != nil {
		t.Fatal("could not render search component environment")
	}
	files, err := f.target.renderFiles(ctx, kind, run)
	if err != nil {
		t.Fatal("could not render search runtime files")
	}
	const ref = "org/env/search/runtime_external_search_qualification"
	envJSON, err := json.Marshal(run.Env)
	if err != nil || f.secrets.Put(ctx, ref, envJSON) != nil || f.target.keepRuntimeFiles(ctx, ref, files) != nil {
		t.Fatal("could not retain private search runtime values")
	}
	labels := map[string]string{owned.LabelOrg: "org", owned.LabelEnvironment: "env", owned.LabelService: "search", owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true", owned.LabelCatalogResource: "search", owned.LabelCatalogKind: kind.Name, owned.LabelCatalogComponent: kind.Name, owned.LabelHealthChecker: kind.Name + "-v1"}
	values, err := f.target.values(owned.WithRuntimeIdentity(ctx, owned.RuntimeIdentity{Instance: iid, Provider: "server"}), owned.InstanceLabels(labels, iid))
	if err != nil {
		t.Fatal("could not resolve instance-specific search files")
	}
	var env strings.Builder
	for _, key := range slices.Sorted(maps.Keys(run.Env)) {
		value := run.Env[key]
		if strings.ContainsAny(key, "=\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
			t.Fatal("search runtime environment cannot use Docker's env file")
		}
		fmt.Fprintf(&env, "%s=%s\n", key, value)
	}
	envFile := filepath.Join(directory, "env")
	if err := os.WriteFile(envFile, []byte(env.String()), 0o600); err != nil {
		t.Fatal("could not write private search environment")
	}
	mounts := []string{"--env-file", envFile}
	for index, path := range slices.Sorted(maps.Keys(values.Files)) {
		file := filepath.Join(directory, fmt.Sprintf("runtime-%d", index))
		if err := os.WriteFile(file, values.Files[path], values.FileMode(path)); err != nil {
			t.Fatal("could not write private search runtime file")
		}
		mounts = append(mounts, "--mount", "type=bind,src="+file+",dst="+path+",readonly")
	}
	access := catalogconnect.EngineAccess{Engine: kind.Name, Host: "localhost", Port: 9200, Username: "elastic", Password: string(run.Secrets["password"]), TLS: catalogconnect.TLSDisable}
	if kind.Name == catalogconnect.EngineOpenSearch {
		const prefix = "/usr/share/opensearch/config/"
		access.Username, access.TLS, access.CACert = "loomplane", catalogconnect.TLSVerifyFull, string(values.Files[prefix+"ca.pem"])
		leaf, _ := pem.Decode(values.Files[prefix+"node.pem"])
		if leaf == nil || access.CACert == "" {
			t.Fatal("OpenSearch runtime trust files are missing")
		}
		cert, certErr := x509.ParseCertificate(leaf.Bytes)
		if certErr != nil || cert.VerifyHostname(access.Host) != nil {
			t.Fatal("OpenSearch runtime certificate does not name the logical host")
		}
	}
	return access, mounts
}

type externalSearchProbeObservation struct {
	stage  string
	result string
}

func observeExternalSearchProbe(dials, connected, responded int32, report *catalogconnect.Report, err error) externalSearchProbeObservation {
	observation := externalSearchProbeObservation{stage: "request-setup"}
	switch dials {
	case 0:
	case 1:
		observation.stage = "product"
	case 2:
		observation.stage = "identity"
	case 3:
		observation.stage = "health"
	default:
		return externalSearchProbeObservation{stage: "unknown", result: "unexpected-dial-count"}
	}
	switch {
	case connected < dials:
		observation.result = "dial-failed"
	case responded < dials:
		observation.result = "no-response"
	case err != nil:
		switch err.Error() {
		case "the search application credentials or required read permission were refused":
			observation.result = "access-refused"
		case "the search product version could not be verified", "the endpoint is not a supported Elasticsearch 9.4 product", "the endpoint is not a supported OpenSearch 3.9 product":
			observation.result = "product-refused"
		case "the search application identity could not be verified":
			observation.result = "identity-refused"
		case "the search cluster-health read did not confirm an available cluster":
			observation.result = "health-unavailable"
		case "the fixed search request was refused":
			observation.result = "request-refused"
		case "the search response exceeded the check bounds or was incomplete", "the search response could not be verified":
			observation.result = "response-invalid"
		default:
			observation.result = "probe-error"
		}
	case report == nil:
		observation.result = "missing-report"
	case !report.Result().OK:
		observation.result = "report-refused"
	default:
		// A usable report with the expected product version has already passed.
		observation.result = "version-mismatch"
	}
	return observation
}

func externalSearchContainerState(ctx context.Context, docker externalSearchDocker, expected externalSearchObject) string {
	const format = `{"ID":{{json .Id}},"Name":{{json .Name}},"Scope":{{json (index .Config.Labels "loomplane.qualification.search")}},"Created":{{json .Created}},"Running":{{json .State.Running}},"ExitCode":{{json .State.ExitCode}},"OOMKilled":{{json .State.OOMKilled}}}`
	raw, err := docker(ctx, "container", "inspect", "--format", format, expected.ID)
	var state struct {
		ID, Name, Scope, Created string
		Running, OOMKilled       *bool
		ExitCode                 *int
	}
	if err != nil || json.Unmarshal(raw, &state) != nil || state.ID != expected.ID || strings.TrimPrefix(state.Name, "/") != expected.Name || state.Scope != expected.Scope || state.Created != expected.Created || state.Running == nil || state.ExitCode == nil || state.OOMKilled == nil {
		return "unavailable"
	}
	return fmt.Sprintf("running=%t exit_code=%d oom_killed=%t", *state.Running, *state.ExitCode, *state.OOMKilled)
}

func externalSearchRuntimeMode(daemonOS, daemonArchitecture, imageArchitecture, nativeOnly string) (string, error) {
	if daemonOS != "linux" {
		return "", errors.New("search qualification requires a Linux Docker daemon")
	}
	switch daemonArchitecture {
	case "x86_64", "amd64":
		daemonArchitecture = "amd64"
	case "aarch64", "arm64":
		daemonArchitecture = "arm64"
	default:
		return "", errors.New("search qualification requires an amd64 or arm64 Docker daemon")
	}
	if imageArchitecture != "amd64" && imageArchitecture != "arm64" {
		return "", errors.New("search qualification requires an amd64 or arm64 image")
	}
	if nativeOnly != "" && nativeOnly != "1" {
		return "", errors.New("LOOMPLANE_SEARCH_NATIVE_ONLY must be unset or 1")
	}
	if daemonArchitecture == imageArchitecture {
		return "native", nil
	}
	if nativeOnly == "1" {
		return "", errors.New("native search qualification requires matching daemon and image architectures")
	}
	return "emulated", nil
}

func TestSetupAndQualifyExternalSearchNative(t *testing.T) {
	if os.Getenv("LOOMPLANE_SEARCH_SETUP") != "1" {
		t.Skip("explicit disposable search setup required")
	}
	engine, architecture := os.Getenv("LOOMPLANE_ENGINE"), os.Getenv("LOOMPLANE_ENGINE_ARCH")
	socket, binary := os.Getenv("LOOMPLANE_TEST_DOCKER_SOCKET"), os.Getenv("LOOMPLANE_SEARCH_QUALIFICATION_BIN")
	if !catalogconnect.IsEngineAccessEngine(engine) || architecture != "arm64" && architecture != "amd64" || !filepath.IsAbs(socket) || !filepath.IsAbs(binary) {
		t.Fatal("select one search engine, architecture, local socket and qualification binary")
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("explicit Docker endpoint is not a local Unix socket")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() || info.Mode()&0o100 == 0 {
		t.Fatal("search qualification binary is not executable")
	}
	kind, ok := recipes.Default().Kind(engine)
	if !ok || len(kind.Releases) != 1 {
		t.Fatal("current search recipe must have one pinned release")
	}
	registry := recipes.NewRegistry()
	if err := registry.Register(kind); err != nil {
		t.Fatal("could not register pinned search recipe")
	}
	comp, err := registry.Resolve(engine, "", architecture, "server")
	if err != nil || len(comp.Components) != 1 || comp.Components[0].Volume == nil {
		t.Fatal("could not resolve one stateful search component")
	}
	component := comp.Components[0]
	release := kind.Releases[0]
	image := release.Images[engine]
	if image.Digests[architecture] == "" || component.Image != image.Repository+"@"+image.Digests[architecture] {
		t.Fatal("resolved search runtime does not match the recipe pin")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Minute)
	defer cancel()
	directory := t.TempDir()
	dockerConfig := filepath.Join(directory, "docker")
	if err := os.Mkdir(dockerConfig, 0o700); err != nil {
		t.Fatal("could not create private Docker CLI configuration directory")
	}
	safeEnv := []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig, "GOMAXPROCS=2"}
	docker := func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...)
		cmd.Env = safeEnv
		return cmd.Output()
	}
	var daemon struct{ OS, Architecture string }
	daemonInfo, err := docker(ctx, "info", "--format", `{"OS":{{json .OSType}},"Architecture":{{json .Architecture}}}`)
	if err != nil || json.Unmarshal(daemonInfo, &daemon) != nil {
		t.Fatal("could not verify the search Docker daemon platform")
	}
	mode, err := externalSearchRuntimeMode(daemon.OS, daemon.Architecture, architecture, os.Getenv("LOOMPLANE_SEARCH_NATIVE_ONLY"))
	if err != nil {
		t.Fatal(err)
	}
	var pinned struct{ ID, Architecture, OS string }
	metadata, err := docker(ctx, "image", "inspect", "--format", `{"ID":{{json .Id}},"Architecture":{{json .Architecture}},"OS":{{json .Os}}}`, component.Image)
	if err != nil || json.Unmarshal(metadata, &pinned) != nil || pinned.ID == "" || pinned.Architecture != architecture || pinned.OS != "linux" {
		t.Fatal("exact selected search image is not cached on the local daemon")
	}
	baseline, err := captureExternalSearchObjects(ctx, docker, "")
	if err != nil {
		t.Fatal(err)
	}
	iid := id.New(id.PrefixInstance)
	scope := "lp-task6b1-" + iid.String()
	label := externalSearchSetupLabel + "=" + scope
	objects := make([]externalSearchObject, 0, 3)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		ownedAbsent, foreignPresent := true, true
		for _, object := range slices.Backward(objects) {
			if err := removeExternalSearchObject(cleanup, docker, object, baseline); err != nil {
				t.Errorf("%v for %s", err, object.ID)
				ownedAbsent = false
				break
			}
		}
		remaining, remainErr := captureExternalSearchObjects(cleanup, docker, label)
		after, afterErr := captureExternalSearchObjects(cleanup, docker, "")
		if remainErr != nil || afterErr != nil {
			t.Error("could not verify qualification cleanup identities")
			return
		}
		for _, object := range objects {
			if slices.Contains(after[object.Kind], object.ID) {
				t.Errorf("recorded qualification %s still exists: %s", object.Kind, object.ID)
				ownedAbsent = false
			}
		}
		for _, kind := range []string{"container", "volume", "network"} {
			if len(remaining[kind]) != 0 {
				t.Errorf("qualification %s IDs remain: %v", kind, remaining[kind])
				ownedAbsent = false
			}
			missing := 0
			for _, foreignID := range baseline[kind] {
				if !slices.Contains(after[kind], foreignID) {
					missing++
				}
			}
			if missing != 0 {
				t.Errorf("%d baseline %s identities are absent", missing, kind)
				foreignPresent = false
			}
		}
		t.Logf("cleanup scope=%s owned_absent=%t baseline_foreign_present=%t", scope, ownedAbsent, foreignPresent)
	})
	create := func(kind, name string, args ...string) externalSearchObject {
		t.Helper()
		raw, createErr := docker(ctx, args...)
		object := externalSearchObject{Kind: kind, Name: name, ID: strings.TrimSpace(string(raw)), Scope: scope}
		if createErr != nil || object.ID == "" || strings.ContainsAny(object.ID, " \t\r\n") {
			t.Fatalf("could not create qualification %s for %s", kind, scope)
		}
		objects = append(objects, object)
		actual, inspectErr := inspectExternalSearchObject(ctx, docker, kind, object.ID)
		if inspectErr != nil || !ownedExternalSearchObject(actual, object, baseline) {
			t.Fatalf("could not establish qualification %s identity for %s", kind, scope)
		}
		objects[len(objects)-1] = actual
		return actual
	}

	access, mounts := externalSearchRuntimeFiles(t, ctx, kind, registry, comp, architecture, iid, directory)
	raw, err := catalogconnect.EncodeEngineAccess(access)
	if err != nil {
		t.Fatal("could not encode private qualification access")
	}
	network := create("network", scope+"-net", "network", "create", "--driver", "bridge", "--opt", "com.docker.network.bridge.host_binding_ipv4=127.0.0.1", "--label", label, scope+"-net")
	volume := create("volume", scope+"-data", "volume", "create", "--label", label, scope+"-data")
	containerName := scope + "-server"
	args := []string{"container", "create", "--pull=never", "--platform", "linux/" + architecture, "--name", containerName, "--label", label, "--network", network.ID, "--memory", "2g", "--cpus", "2", "--publish", "127.0.0.1::9200/tcp", "--mount", "type=volume,src=" + volume.ID + ",dst=" + component.Volume.MountPath}
	args = append(args, mounts...)
	if len(component.Command) != 0 {
		args = append(args, "--entrypoint", component.Command[0])
	}
	args = append(args, component.Image)
	if len(component.Command) > 1 {
		args = append(args, component.Command[1:]...)
	}
	args = append(args, component.Args...)
	container := create("container", containerName, args...)
	if _, err := docker(ctx, "container", "start", container.ID); err != nil {
		t.Fatal("could not start owned search qualification container")
	}
	var active struct {
		Image        string
		Running      bool
		Memory, CPUs int64
		Networks     map[string]struct{ NetworkID string }
		Ports        map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	}
	metadata, err = docker(ctx, "container", "inspect", "--format", `{"Image":{{json .Image}},"Running":{{json .State.Running}},"Memory":{{json .HostConfig.Memory}},"CPUs":{{json .HostConfig.NanoCpus}},"Ports":{{json .NetworkSettings.Ports}},"Networks":{{json .NetworkSettings.Networks}}}`, container.ID)
	if err != nil || json.Unmarshal(metadata, &active) != nil || active.Image != pinned.ID || !active.Running || active.Memory != 2<<30 || active.CPUs != 2_000_000_000 {
		t.Fatal("running search container does not match its image and resource limits")
	}
	if len(active.Networks) != 1 || active.Networks[network.Name].NetworkID != network.ID {
		t.Fatal("search qualification container joined an unexpected network")
	}
	for port, bindings := range active.Ports {
		if port != "9200/tcp" && len(bindings) != 0 {
			t.Fatal("qualification container published an unexpected port")
		}
	}
	bindings := active.Ports["9200/tcp"]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
		t.Fatal("qualification container needs one direct loopback binding")
	}
	port, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("qualification container loopback port is invalid")
	}
	address := net.JoinHostPort("127.0.0.1", bindings[0].HostPort)
	authority := net.JoinHostPort(access.Host, strconv.Itoa(access.Port))
	dial := func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" || target != authority {
			return nil, errors.New("unexpected search qualification authority")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	wait := 3 * time.Minute
	if architecture == "amd64" {
		wait = 5 * time.Minute
	}
	ready, stop := context.WithTimeout(ctx, wait)
	defer stop()
	const maxObservations = 8
	observations := make(map[externalSearchProbeObservation]int, maxObservations)
	additional := 0
	for {
		var dials, connected, responded atomic.Int32
		probeContext := httptrace.WithClientTrace(ready, &httptrace.ClientTrace{GotFirstResponseByte: func() { responded.Add(1) }})
		observedDial := func(ctx context.Context, network, target string) (net.Conn, error) {
			dials.Add(1)
			conn, err := dial(ctx, network, target)
			if err == nil {
				connected.Add(1)
			}
			return conn, err
		}
		report, probeErr := (catalogconnect.SearchProber{}).Probe(probeContext, observedDial, raw, recipes.Requirement{Engine: engine, TLS: []string{access.TLS}})
		if probeErr == nil && report != nil && report.Result().OK && report.Version == release.Product {
			break
		}
		observation := observeExternalSearchProbe(dials.Load(), connected.Load(), responded.Load(), report, probeErr)
		if observations[observation] != 0 || len(observations) < maxObservations {
			observations[observation]++
		} else {
			additional++
		}
		select {
		case <-ready.Done():
			for item, count := range observations {
				t.Logf("search readiness probe_stage=%s probe_result=%s attempts=%d", item.stage, item.result, count)
			}
			if additional > 0 {
				t.Logf("search readiness additional_attempts=%d", additional)
			}
			stateContext, finishState := context.WithTimeout(ctx, 2*time.Second)
			t.Logf("search readiness container_state %s", externalSearchContainerState(stateContext, docker, container))
			finishState()
			t.Fatal("owned search runtime did not pass authenticated readiness within its startup bound")
		case <-time.After(time.Second):
		}
	}
	stop()
	child, finish := context.WithTimeout(ctx, 45*time.Second)
	defer finish()
	cmd := exec.CommandContext(child, binary, "-test.run=^TestQualifyExternalSearchNative$", "-test.v", "-test.count=1", "-test.timeout=45s")
	cmd.Env = append(slices.Clone(safeEnv), "LOOMPLANE_SEARCH_NATIVE=1", "LOOMPLANE_TEST_DOCKER_SOCKET="+socket, "LOOMPLANE_ENGINE_ARCH="+architecture, "LOOMPLANE_SEARCH_CONTAINER="+container.ID, "LOOMPLANE_SEARCH_NATIVE_ADDRESS="+address, "LOOMPLANE_SEARCH_ACCESS="+raw)
	result, err := cmd.CombinedOutput()
	marker := fmt.Sprintf("verified engine=%s architecture=%s product=%s authenticated_identity=true cluster_health_read=true", engine, architecture, release.Product)
	if err != nil || !bytes.Contains(result, []byte(marker)) {
		t.Fatal("pinned external search fixture failed its product, identity, health or bad-password check")
	}
	t.Logf("engine=%s product=%s architecture=%s mode=%s daemon=%s digest=%s image=%s container=%s address=%s guarded_requests=3 authenticated_identity=true cluster_health_read=true bad_password_refused=true", engine, release.Product, architecture, mode, daemon.Architecture, image.Digests[architecture], pinned.ID, container.ID, address)
}

func TestExternalSearchSetupCleanupChecksIdentity(t *testing.T) {
	container := externalSearchObject{Kind: "container", ID: "owned-id", Name: "owned", Scope: "phase", Created: "creation"}
	volume := externalSearchObject{Kind: "volume", ID: "owned-data", Name: "owned-data", Scope: "phase", Created: "creation"}
	for _, tc := range []struct {
		name        string
		expected    externalSearchObject
		observed    externalSearchObject
		baseline    externalSearchSnapshot
		wantRemoval []string
	}{
		{name: "exact owned container", expected: container, observed: container, wantRemoval: []string{"container", "rm", "--force", container.ID}},
		{name: "exact owned volume", expected: volume, observed: volume, wantRemoval: []string{"volume", "rm", volume.ID}},
		{name: "foreign scope", expected: container, observed: externalSearchObject{ID: container.ID, Name: container.Name, Scope: "foreign", Created: container.Created}},
		{name: "replacement ID", expected: container, observed: externalSearchObject{ID: "replacement", Name: container.Name, Scope: container.Scope, Created: container.Created}},
		{name: "recreated volume", expected: volume, observed: externalSearchObject{ID: volume.ID, Name: volume.Name, Scope: volume.Scope, Created: "later"}},
		{name: "baseline container", expected: container, observed: container, baseline: externalSearchSnapshot{"container": {container.ID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var removed []string
			docker := func(_ context.Context, args ...string) ([]byte, error) {
				if args[1] == "inspect" {
					if args[0] != tc.expected.Kind || args[len(args)-1] != tc.expected.ID {
						t.Error("cleanup inspected the wrong object")
					}
					return json.Marshal(tc.observed)
				}
				removed = append(removed, args...)
				return nil, nil
			}
			err := removeExternalSearchObject(t.Context(), docker, tc.expected, tc.baseline)
			if len(tc.wantRemoval) != 0 {
				if err != nil || !slices.Equal(removed, tc.wantRemoval) {
					t.Fatal("cleanup did not remove the exact owned object")
				}
			} else if err == nil || len(removed) != 0 {
				t.Fatal("cleanup tried to remove an unverified object")
			}
		})
	}
}

func TestExternalSearchSetupPreparesRuntimeFiles(t *testing.T) {
	for _, tc := range []struct {
		engine string
		tls    string
	}{
		{catalogconnect.EngineElasticsearch, catalogconnect.TLSDisable},
		{catalogconnect.EngineOpenSearch, catalogconnect.TLSVerifyFull},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			kind, ok := recipes.Default().Kind(tc.engine)
			if !ok {
				t.Fatal("search recipe missing")
			}
			registry := recipes.NewRegistry()
			if err := registry.Register(kind); err != nil {
				t.Fatal("could not register search recipe")
			}
			comp, err := registry.Resolve(tc.engine, "", "arm64", "server")
			if err != nil || len(comp.Components) != 1 {
				t.Fatal("could not resolve one search component")
			}
			directory := t.TempDir()
			access, mounts := externalSearchRuntimeFiles(t, t.Context(), kind, registry, comp, "arm64", id.New(id.PrefixInstance), directory)
			if access.Engine != tc.engine || access.Host != "localhost" || access.Port != 9200 || access.TLS != tc.tls {
				t.Fatal("search runtime connection metadata differs from its recipe")
			}
			want := make(map[string]bool)
			for _, file := range comp.Components[0].Files {
				want[file.Path] = true
			}
			envPath := filepath.Join(directory, "env")
			if len(want) == 0 || len(mounts) != 2+2*len(want) || mounts[0] != "--env-file" || mounts[1] != envPath {
				t.Fatal("search runtime did not stage its declared file metadata")
			}
			if info, err := os.Stat(envPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatal("search runtime env file must be private")
			}
			for index := 2; index < len(mounts); index += 2 {
				fields := strings.Split(mounts[index+1], ",")
				if mounts[index] != "--mount" || len(fields) != 4 || fields[0] != "type=bind" || fields[3] != "readonly" {
					t.Fatal("search runtime file is not a read-only bind mount")
				}
				local, sourceOK := strings.CutPrefix(fields[1], "src=")
				target, targetOK := strings.CutPrefix(fields[2], "dst=")
				if !sourceOK || filepath.Dir(local) != directory || !targetOK || !want[target] {
					t.Fatal("search runtime mount is outside its declared files")
				}
				if info, err := os.Stat(local); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
					t.Fatal("search runtime mount source has invalid file metadata")
				}
				delete(want, target)
			}
		})
	}
}

func TestExternalSearchSetupReadinessDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		dials, connected, responded int32
		report                      *catalogconnect.Report
		err                         error
		want                        externalSearchProbeObservation
	}{
		{name: "dial failure", dials: 1, err: errors.New("untrusted peer detail"), want: externalSearchProbeObservation{"product", "dial-failed"}},
		{name: "no response", dials: 1, connected: 1, err: errors.New("untrusted peer detail"), want: externalSearchProbeObservation{"product", "no-response"}},
		{name: "identity refusal", dials: 2, connected: 2, responded: 2, err: errors.New("the search application credentials or required read permission were refused"), want: externalSearchProbeObservation{"identity", "access-refused"}},
		{name: "health unavailable", dials: 3, connected: 3, responded: 3, err: errors.New("the search cluster-health read did not confirm an available cluster"), want: externalSearchProbeObservation{"health", "health-unavailable"}},
		{name: "unrecognized response error", dials: 1, connected: 1, responded: 1, err: errors.New("untrusted peer detail"), want: externalSearchProbeObservation{"product", "probe-error"}},
		{name: "untrusted report text", report: &catalogconnect.Report{Problems: []string{"untrusted peer detail"}}, want: externalSearchProbeObservation{"request-setup", "report-refused"}},
		{name: "untrusted version text", dials: 3, connected: 3, responded: 3, report: &catalogconnect.Report{Version: "untrusted peer detail"}, want: externalSearchProbeObservation{"health", "version-mismatch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeExternalSearchProbe(tc.dials, tc.connected, tc.responded, tc.report, tc.err); got != tc.want {
				t.Fatal("readiness observation did not use the expected fixed labels")
			}
		})
	}

	object := externalSearchObject{Kind: "container", ID: "owned-id", Name: "owned", Scope: "phase", Created: "creation"}
	for _, tc := range []struct {
		name, state, want string
	}{
		{"owned state", `{"ID":"owned-id","Name":"/owned","Scope":"phase","Created":"creation","Running":false,"ExitCode":137,"OOMKilled":true,"Extra":"untrusted peer detail"}`, "running=false exit_code=137 oom_killed=true"},
		{"different scope", `{"ID":"owned-id","Name":"/owned","Scope":"other","Created":"creation","Running":true,"ExitCode":0,"OOMKilled":false}`, "unavailable"},
		{"missing state", `{"ID":"owned-id","Name":"/owned","Scope":"phase","Created":"creation","Running":true}`, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var removed []string
			docker := func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] != "container" || args[len(args)-1] != object.ID {
					t.Fatal("readiness diagnostics or cleanup selected an unexpected object")
				}
				if args[1] == "inspect" {
					if strings.Contains(args[3], ".State.Running") {
						return []byte(tc.state), nil
					}
					return json.Marshal(object)
				}
				removed = append(removed, args...)
				return nil, nil
			}
			if got := externalSearchContainerState(t.Context(), docker, object); got != tc.want || len(removed) != 0 {
				t.Fatal("state diagnostic returned unexpected data or tried to mutate the container")
			}
			if err := removeExternalSearchObject(t.Context(), docker, object, nil); err != nil || !slices.Equal(removed, []string{"container", "rm", "--force", object.ID}) {
				t.Fatal("state diagnostic did not preserve the guarded cleanup")
			}
		})
	}
}

func TestExternalSearchSetupRuntimeMode(t *testing.T) {
	for _, tc := range []struct {
		name, daemonOS, daemonArchitecture, imageArchitecture, nativeOnly, want string
	}{
		{"Linux x86_64 native", "linux", "x86_64", "amd64", "1", "native"},
		{"Linux amd64 native", "linux", "amd64", "amd64", "1", "native"},
		{"Linux aarch64 native", "linux", "aarch64", "arm64", "1", "native"},
		{"Linux arm64 native", "linux", "arm64", "arm64", "1", "native"},
		{"amd64 emulated", "linux", "aarch64", "amd64", "", "emulated"},
		{"arm64 emulated", "linux", "x86_64", "arm64", "", "emulated"},
		{"native-only rejects emulation", "linux", "aarch64", "amd64", "1", ""},
		{"invalid native-only value", "linux", "x86_64", "amd64", "0", ""},
		{"non-Linux daemon", "windows", "x86_64", "amd64", "1", ""},
		{"unknown daemon architecture", "linux", "riscv64", "amd64", "", ""},
		{"unknown image architecture", "linux", "x86_64", "386", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, err := externalSearchRuntimeMode(tc.daemonOS, tc.daemonArchitecture, tc.imageArchitecture, tc.nativeOnly)
			if mode != tc.want || (err == nil) != (tc.want != "") {
				t.Fatal("qualification accepted or mislabeled a runtime platform")
			}
		})
	}
}
