package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	nativemysql "github.com/xraph/loomplane/internal/clouds/mysql"
	nativenats "github.com/xraph/loomplane/internal/clouds/nats"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativerabbit "github.com/xraph/loomplane/internal/clouds/rabbitmq"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
)

type mutationContainer struct {
	containerInfo
	Name       string
	HostConfig struct {
		NetworkMode    string
		ReadonlyRootfs bool
	}
}

type mutationFixture struct {
	mu                                    sync.Mutex
	runtime                               *Runtime
	client                                *Client
	iid                                   id.ID
	labels                                map[string]string
	containers                            map[string]*mutationContainer
	volumes                               map[string]instanceClassVolume
	sourceEffects, starts, probes, claims int
	failStart, failProbe, failStop        bool
	walStatus, exit                       int
	onClaim                               func(*mutationFixture)
	onHead                                func(*mutationFixture)
	beforeRequest                         func(*http.Request)
}

func newMutationFixture(t *testing.T, private bool) *mutationFixture {
	t.Helper()
	f := &mutationFixture{iid: id.New(id.PrefixInstance), containers: map[string]*mutationContainer{}, volumes: map[string]instanceClassVolume{}, walStatus: http.StatusNotFound}
	f.labels = map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "target", owned.LabelService: "analytics", owned.LabelArchitecture: "arm64", owned.LabelRevision: "deployment", owned.LabelRuntime: "runtime", owned.LabelImage: "local/duckdb@sha256:" + strings.Repeat("a", 64), enginebackup.LabelEngine: "duckdb", enginebackup.LabelRevision: "2", owned.LabelPublic: "false"}
	if private {
		f.labels[LabelRecovery] = "restore-operation"
		original, err := engineIdentity(id.New(id.PrefixInstance), f.labels)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		f.labels[enginebackup.LabelSource] = string(raw)
	}
	labels := maps.Clone(f.labels)
	labels[owned.LabelInstance] = f.iid.String()
	source := &mutationContainer{Name: "/" + ContainerName(f.iid)}
	source.ID = "source"
	source.Config.Image = f.labels[owned.LabelImage]
	source.Config.Labels = labels
	source.State.Running = true
	source.State.Status = "running"
	source.Mounts = append(source.Mounts, struct{ Type, Name, Destination string }{"volume", VolumeName(f.iid, "data"), "/data"})
	f.containers[ContainerName(f.iid)] = source
	anchor, err := classLabels(f.iid, f.labels, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	f.volumes[instanceClassName(f.iid)] = instanceClassVolume{Name: instanceClassName(f.iid), Driver: "local", Scope: "local", CreatedAt: "anchor-created", Labels: anchor}
	f.volumes[VolumeName(f.iid, "data")] = instanceClassVolume{Name: VolumeName(f.iid, "data"), Driver: "local", CreatedAt: "data-created", Labels: map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "target", owned.LabelInstance: f.iid.String(), labelVolumeRole: "data"}}
	api := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(api.Close)
	f.client = &Client{Config: &Config{}, HTTP: &http.Client{Transport: redirectDocker{base: api.URL, next: api.Client().Transport}}}
	var ticks atomic.Int64
	f.runtime = &Runtime{Wait: time.Millisecond, Now: func() time.Time { return time.Unix(ticks.Add(1), 0) }, Session: func(context.Context, map[string]string) (*Client, error) { return f.client, nil }}
	return f
}

func (f *mutationFixture) change(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }
func (f *mutationFixture) find(key string) (string, *mutationContainer) {
	for name, item := range f.containers {
		if name == key || item.ID == key {
			return name, item
		}
	}
	return "", nil
}
func (f *mutationFixture) held() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers[ContainerName(f.iid)+"-mutation"] != nil
}
func (f *mutationFixture) source() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[ContainerName(f.iid)]
	return c != nil, c != nil && c.State.Running
}
func (f *mutationFixture) serve(w http.ResponseWriter, r *http.Request) {
	if f.beforeRequest != nil {
		f.beforeRequest(r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/volumes/create" && r.Method == http.MethodPost:
		var v instanceClassVolume
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			w.WriteHeader(400)
			return
		}
		if old, ok := f.volumes[v.Name]; ok {
			v = old
		} else {
			v.CreatedAt = "created-" + v.Name
			v.Scope = "local"
			f.volumes[v.Name] = v
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(v)
	case strings.HasPrefix(p, "/volumes/"):
		key := strings.TrimPrefix(p, "/volumes/")
		v, ok := f.volumes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(v)
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.volumes, key)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(405)
	case strings.HasPrefix(p, "/images/"):
		_, _ = io.WriteString(w, `{}`)
	case p == "/networks/create":
		_, _ = io.WriteString(w, `{"Id":"network"}`)
	case strings.HasPrefix(p, "/networks/"):
		http.NotFound(w, r)
	case p == "/containers/create":
		name := r.URL.Query().Get("name")
		if f.containers[name] != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		var body struct {
			Image      string
			Labels     map[string]string
			HostConfig struct {
				NetworkMode    string
				ReadonlyRootfs bool
				Mounts         []volumeMount
			}
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		c := &mutationContainer{Name: "/" + name}
		c.Config.Image = body.Image
		c.Config.Labels = body.Labels
		c.HostConfig.NetworkMode = body.HostConfig.NetworkMode
		c.HostConfig.ReadonlyRootfs = body.HostConfig.ReadonlyRootfs
		if strings.HasSuffix(name, "-mutation") {
			f.claims++
			c.ID = "reservation"
			c.State.Status = "created"
		} else {
			f.sourceEffects++
			c.ID = "candidate"
			c.State.Status = "created"
			for _, mount := range body.HostConfig.Mounts {
				c.Mounts = append(c.Mounts, struct{ Type, Name, Destination string }{mount.Type, mount.Source, mount.Target})
			}
		}
		f.containers[name] = c
		if strings.HasSuffix(name, "-mutation") && f.onClaim != nil {
			f.onClaim(f)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": c.ID})
	case p == "/exec/probe/start":
		w.WriteHeader(http.StatusOK)
	case p == "/exec/probe/json":
		exit := 0
		if f.failProbe {
			exit = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Running": false, "ExitCode": exit})
	case strings.HasPrefix(p, "/containers/"):
		parts := strings.Split(strings.TrimPrefix(p, "/containers/"), "/")
		name, c := f.find(parts[0])
		if c == nil {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if !strings.HasSuffix(name, "-mutation") {
				f.sourceEffects++
			}
			delete(f.containers, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		switch parts[1] {
		case "json":
			_ = json.NewEncoder(w).Encode(c)
		case "stop":
			f.sourceEffects++
			c.State.Running = false
			c.State.Status = "exited"
			c.State.ExitCode = f.exit
			if f.failStop {
				w.WriteHeader(500)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case "start":
			f.sourceEffects++
			f.starts++
			if f.failStart {
				w.WriteHeader(500)
				return
			}
			c.State.Running = true
			c.State.Status = "running"
			w.WriteHeader(http.StatusNoContent)
		case "rename":
			f.sourceEffects++
			delete(f.containers, name)
			newName := r.URL.Query().Get("name")
			c.Name = "/" + newName
			f.containers[newName] = c
			w.WriteHeader(http.StatusNoContent)
		case "archive":
			if r.Method != http.MethodHead {
				w.WriteHeader(400)
				return
			}
			if f.onHead != nil {
				f.onHead(f)
			}
			w.WriteHeader(f.walStatus)
		case "exec":
			var body struct{ Cmd []string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || !slices.Equal(body.Cmd, []string{"/loomplane-duckdb", "check"}) {
				w.WriteHeader(400)
				return
			}
			f.probes++
			_, _ = io.WriteString(w, `{"Id":"probe"}`)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func TestInstanceClassConcurrentFirstCreation(t *testing.T) {
	f := newMutationFixture(t, false)
	f.change(func() { clear(f.containers); clear(f.volumes) })
	ordinary := maps.Clone(f.labels)
	delete(ordinary, enginebackup.LabelEngine)
	delete(ordinary, enginebackup.LabelRevision)
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	f.beforeRequest = func(r *http.Request) {
		if r.URL.Path == "/volumes/create" {
			arrived <- struct{}{}
			<-release
		}
	}
	results := make(chan error, 2)
	for _, labels := range []map[string]string{f.labels, ordinary} {
		go func() { _, err := f.runtime.classifyInstance(t.Context(), f.client, f.iid, labels); results <- err }()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("class proposals did not reach atomic create")
		}
	}
	close(release)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("exactly one class must win: %v / %v", first, second)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.volumes) != 1 || f.sourceEffects != 0 {
		t.Fatal("class race changed instance data")
	}
}

func TestInstanceClassAdoptsKnownCurrentAndRejectsAmbiguity(t *testing.T) {
	for _, mode := range []string{"duck", "ordinary", "previous", "stopped-previous", "checkpoint", "mutation", "data-only", "foreign", "changed-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, false)
			delete(f.volumes, instanceClassName(f.iid))
			switch mode {
			case "ordinary":
				delete(f.labels, enginebackup.LabelEngine)
				delete(f.labels, enginebackup.LabelRevision)
				delete(f.containers[ContainerName(f.iid)].Config.Labels, enginebackup.LabelEngine)
				delete(f.containers[ContainerName(f.iid)].Config.Labels, enginebackup.LabelRevision)
			case "previous", "stopped-previous", "checkpoint", "mutation":
				suffix := map[string]string{"previous": "-previous", "stopped-previous": "-previous-stopped", "checkpoint": "-checkpoint", "mutation": "-mutation"}[mode]
				f.containers[ContainerName(f.iid)+suffix] = &mutationContainer{containerInfo: containerInfo{ID: "ambiguous"}}
			case "data-only":
				delete(f.containers, ContainerName(f.iid))
			case "foreign":
				f.containers[ContainerName(f.iid)].Config.Labels[owned.LabelConnection] = "other"
			case "changed-source":
				var calls atomic.Int64
				f.beforeRequest = func(r *http.Request) {
					if r.URL.Path == "/containers/"+ContainerName(f.iid)+"/json" && calls.Add(1) == 2 {
						f.change(func() { f.containers[ContainerName(f.iid)].ID = "replacement" })
					}
				}
			}
			_, err := f.runtime.classifyInstance(t.Context(), f.client, f.iid, f.labels)
			allowed := mode == "duck" || mode == "ordinary"
			if (err == nil) != allowed {
				t.Fatalf("adoption result: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.sourceEffects != 0 {
				t.Fatal("adoption mutated source")
			}
			if !allowed && mode != "changed-source" && f.volumes[instanceClassName(f.iid)].Name != "" {
				t.Fatal("ambiguous evidence created anchor")
			}
		})
	}
}

func TestInstanceClassRejectsMalformedOrMismatchedAnchor(t *testing.T) {
	for _, mode := range []string{"name", "driver", "options", "schema", "class", "org", "project", "environment", "connection", "instance", "resource", "recovery", "discovery"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, false)
			name := instanceClassName(f.iid)
			v := f.volumes[name]
			switch mode {
			case "name":
				v.Name = "different"
			case "driver":
				v.Driver = "nfs"
			case "options":
				v.Options = map[string]string{"device": "/foreign"}
			case "discovery":
				v.Labels[owned.LabelInstance] = f.iid.String()
			default:
				v.Labels[instanceClassLabel+mode] = "other"
			}
			f.volumes[name] = v
			if err := f.runtime.Remove(t.Context(), f.iid, f.labels); err == nil {
				t.Fatal("malformed anchor accepted")
			}
			if f.sourceEffects != 0 || f.held() {
				t.Fatal("anchor rejection mutated instance")
			}
		})
	}
}

func TestInstanceClassClosesMissingCurrentAndAliasGaps(t *testing.T) {
	for _, mode := range []string{"absent", "renamed", "purge-gap", "alias", "ordinary-anchor-duck-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, true)
			labels := maps.Clone(f.labels)
			if mode == "alias" {
				labels[owned.LabelConnection] = "alias"
			} else {
				delete(labels, enginebackup.LabelEngine)
			}
			if mode == "ordinary-anchor-duck-source" {
				v := f.volumes[instanceClassName(f.iid)]
				v.Labels[instanceClassLabel+"class"] = "ordinary"
				f.volumes[v.Name] = v
			} else if mode != "alias" {
				if mode == "renamed" {
					f.containers[ContainerName(f.iid)+"-previous"] = f.containers[ContainerName(f.iid)]
				}
				delete(f.containers, ContainerName(f.iid))
			}
			if mode == "purge-gap" {
				f.containers[ContainerName(f.iid)+"-mutation"] = &mutationContainer{containerInfo: containerInfo{ID: "held"}}
			}
			if err := f.runtime.PurgeRecovery(t.Context(), f.iid, labels); err == nil {
				t.Fatal("class or alias bypassed reservation")
			}
			if f.sourceEffects != 0 || len(f.volumes) != 2 {
				t.Fatal("gap removed source residue")
			}
		})
	}
}

func TestDuckMutationBlocksLifecycleAndApplyVersusPurge(t *testing.T) {
	for _, action := range []string{"backup", "apply"} {
		t.Run(action, func(t *testing.T) {
			f := newMutationFixture(t, true)
			if action == "backup" {
				delete(f.labels, LabelRecovery)
				delete(f.labels, enginebackup.LabelSource)
				f.containers[ContainerName(f.iid)].Config.Labels = owned.InstanceLabels(f.labels, f.iid)
				v := f.volumes[instanceClassName(f.iid)]
				v.Labels[instanceClassLabel+"recovery"] = ""
				f.volumes[v.Name] = v
			}
			guard, err := f.runtime.acquireDuckMutation(t.Context(), f.client, f.iid, f.labels, duckMutationIntent{Action: action, Image: f.labels[owned.LabelImage], Revision: f.labels[owned.LabelRevision]})
			if err != nil {
				t.Fatal(err)
			}
			operations := []func() error{
				func() error {
					_, err := f.runtime.Apply(t.Context(), f.iid, cpprovider.ServiceSpec{Image: f.labels[owned.LabelImage]}, f.labels)
					return err
				},
				func() error { return f.runtime.SetRunning(t.Context(), f.iid, f.labels, false) },
				func() error { return f.runtime.Remove(t.Context(), f.iid, f.labels) },
				func() error { return f.runtime.RemovePending(t.Context(), f.iid, f.labels) },
			}
			if action == "apply" {
				operations = append(operations, func() error { return f.runtime.PurgeRecovery(t.Context(), f.iid, f.labels) })
			}
			for _, operation := range operations {
				if err := operation(); err == nil {
					t.Fatal("lifecycle bypassed held claim")
				}
			}
			if f.sourceEffects != 0 {
				t.Fatal("contention changed source")
			}
			if err := guard.finish(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if f.held() {
				t.Fatal("healthy terminal receipt retained claim")
			}
		})
	}
}

func TestDuckMutationRevalidatesSourceVolumeAndOwner(t *testing.T) {
	for _, mode := range []string{"source", "image", "revision", "connection", "volume", "owner", "anchor", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, false)
			f.onClaim = func(f *mutationFixture) {
				source := f.containers[ContainerName(f.iid)]
				switch mode {
				case "source":
					source.ID = "replacement"
				case "stopped":
					source.State.Running = false
				case "image":
					source.Config.Image = "foreign"
				case "revision":
					source.Config.Labels[owned.LabelRevision] = "changed"
				case "connection":
					source.Config.Labels[owned.LabelConnection] = "changed"
				case "volume":
					v := f.volumes[VolumeName(f.iid, "data")]
					v.CreatedAt = "recreated"
					f.volumes[v.Name] = v
				case "owner":
					f.containers[ContainerName(f.iid)+"-mutation"].Config.Labels[duckMutationRecordLabel] = "changed"
				case "anchor":
					delete(f.volumes, instanceClassName(f.iid))
				}
			}
			if _, err := f.runtime.acquireDuckMutation(t.Context(), f.client, f.iid, f.labels, duckMutationIntent{Action: "backup"}); err == nil {
				t.Fatal("changed claim source accepted")
			}
			if !f.held() || f.sourceEffects != 0 {
				t.Fatal("ambiguous claim cleared or changed source")
			}
		})
	}
}

func TestDuckMutationPreviousIncarnationsStayHeldBeforeRemoval(t *testing.T) {
	for _, action := range []string{"remove", "purge"} {
		f := newMutationFixture(t, true)
		f.containers[ContainerName(f.iid)+"-previous"] = &mutationContainer{containerInfo: containerInfo{ID: "foreign-previous"}}
		var err error
		if action == "purge" {
			err = f.runtime.PurgeRecovery(t.Context(), f.iid, f.labels)
		} else {
			err = f.runtime.Remove(t.Context(), f.iid, f.labels)
		}
		if err == nil || !f.held() || f.sourceEffects != 0 || len(f.volumes) != 2 {
			t.Fatal("previous incarnation was reconciled destructively")
		}
	}
}

func TestDuckMutationMandatoryReadinessAndStopReceipt(t *testing.T) {
	for _, checker := range []string{"", "postgres-v1"} {
		f := newMutationFixture(t, false)
		f.labels[owned.LabelHealthChecker] = checker
		f.failProbe = true
		if err := f.runtime.SetRunning(t.Context(), f.iid, f.labels, true); err == nil {
			t.Fatal("failed native auth accepted")
		}
		if f.probes == 0 || !f.held() {
			t.Fatal("caller health metadata weakened receipt")
		}
	}
	f := newMutationFixture(t, false)
	if err := f.runtime.SetRunning(t.Context(), f.iid, f.labels, false); err != nil {
		t.Fatal(err)
	}
	if _, running := f.source(); running || f.held() || f.probes != 0 {
		t.Fatal("stopped receipt is invalid")
	}
	if err := f.runtime.SetRunning(t.Context(), f.iid, f.labels, true); err != nil {
		t.Fatal(err)
	}
	if _, running := f.source(); !running || f.held() || f.probes == 0 {
		t.Fatal("running receipt is invalid")
	}
}

func TestCheckpointFailureCancellationAndWALResumeExactSource(t *testing.T) {
	for _, mode := range []string{"success", "copy-failure", "cancelled", "unclean", "wal", "unknown-wal", "head-lost-source", "restart", "readiness", "owner", "dirty-fence"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, false)
			switch mode {
			case "unclean":
				f.exit = 137
			case "wal":
				f.walStatus = http.StatusOK
			case "unknown-wal":
				f.walStatus = http.StatusInternalServerError
			case "head-lost-source":
				f.onHead = func(f *mutationFixture) { delete(f.containers, ContainerName(f.iid)) }
			case "restart":
				f.failStart = true
			case "readiness":
				f.failProbe = true
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			err := f.runtime.stoppedEngine(ctx, f.client, f.iid, f.labels, "backup", "object", func(_ context.Context, source string) error {
				called = true
				if source != "source" {
					t.Fatal("copy did not use exact recorded source")
				}
				if _, running := f.source(); running {
					t.Fatal("copied running database")
				}
				switch mode {
				case "cancelled":
					cancel()
					return context.Canceled
				case "copy-failure":
					return errors.New("storage unavailable")
				case "owner":
					f.change(func() {
						f.containers[ContainerName(f.iid)+"-mutation"].Config.Labels[duckMutationRecordLabel] = "other-owner"
					})
				case "dirty-fence":
					f.client.RequireFence = true
				}
				return nil
			})
			if (mode == "success") != (err == nil) {
				t.Fatalf("wrong operation result: %v", err)
			}
			held := mode == "head-lost-source" || mode == "restart" || mode == "readiness" || mode == "owner" || mode == "dirty-fence"
			if f.held() != held {
				t.Fatalf("held=%v, want %v: %v", f.held(), held, err)
			}
			if !held {
				if exists, running := f.source(); !exists || !running || f.starts != 1 {
					t.Fatal("source was not resumed exactly once")
				}
			}
			if (mode == "unclean" || mode == "wal" || mode == "unknown-wal" || mode == "head-lost-source") && called {
				t.Fatal("unsafe database copy started")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.volumes) != 2 {
				t.Fatal("backup removed source data or anchor")
			}
		})
	}
}

func TestFailedRestoreDiscardsOnlyExactPrivateDestination(t *testing.T) {
	for _, mode := range []string{"copy", "start", "probe", "changed-volume", "changed-source", "previous", "owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, true)
			if mode == "start" {
				f.failStart = true
			}
			if mode == "probe" {
				f.failProbe = true
			}
			err := f.runtime.stoppedEngine(t.Context(), f.client, f.iid, f.labels, "restore", "operation", func(context.Context, string) error {
				f.change(func() {
					switch mode {
					case "changed-volume":
						v := f.volumes[VolumeName(f.iid, "data")]
						v.CreatedAt = "replacement"
						f.volumes[v.Name] = v
					case "changed-source":
						f.containers[ContainerName(f.iid)].ID = "replacement"
					case "previous":
						f.containers[ContainerName(f.iid)+"-previous"] = &mutationContainer{containerInfo: containerInfo{ID: "previous"}}
					case "owner":
						f.containers[ContainerName(f.iid)+"-mutation"].Config.Labels[duckMutationRecordLabel] = "replacement"
					}
				})
				if mode == "start" || mode == "probe" {
					return nil
				}
				return errors.New("import failed")
			})
			if err == nil {
				t.Fatal("failed restore accepted")
			}
			discard := mode == "copy" || mode == "start" || mode == "probe"
			exists, _ := f.source()
			if exists == discard || f.held() == discard {
				t.Fatalf("incorrect private discard: exists=%v held=%v", exists, f.held())
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.volumes[instanceClassName(f.iid)].Name == "" {
				t.Fatal("private cleanup deleted immutable anchor")
			}
			if (f.volumes[VolumeName(f.iid, "data")].Name == "") != discard {
				t.Fatal("private volume disposition mismatched")
			}
		})
	}
}

// These ordinary lifecycle fixtures start after class adoption. Only the exact
// immutable anchor GET is added; their existing daemon expectations stay intact.
func seedOrdinaryClass(t *testing.T, r *Runtime, iid id.ID, labels map[string]string) {
	t.Helper()
	expected, err := classLabels(iid, labels, "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(instanceClassVolume{Name: instanceClassName(iid), Driver: "local", Scope: "local", Labels: expected})
	if err != nil {
		t.Fatal(err)
	}
	session := r.Session
	r.Session = func(ctx context.Context, labels map[string]string) (*Client, error) {
		c, err := session(ctx, labels)
		if err != nil {
			return nil, err
		}
		cloned := *c
		client := *c.HTTP
		cloned.HTTP = &client
		next := client.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		client.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodGet && req.URL.Path == "/volumes/"+instanceClassName(iid) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: http.Header{}}, nil
			}
			return next.RoundTrip(req)
		})
		return &cloned, nil
	}
}

func TestLegacyClassUsesOnlyPreviouslyPersistedSourceMetadata(t *testing.T) {
	for _, class := range []string{"ordinary", "duckdb"} {
		f := newMutationFixture(t, false)
		delete(f.volumes, instanceClassName(f.iid))
		source := f.containers[ContainerName(f.iid)]
		for _, key := range []string{owned.LabelService, owned.LabelImage, owned.LabelArchitecture, owned.LabelCatalogKind} {
			delete(source.Config.Labels, key)
		}
		if class == "ordinary" {
			for _, key := range []string{enginebackup.LabelEngine, enginebackup.LabelRevision} {
				delete(f.labels, key)
				delete(source.Config.Labels, key)
			}
		}
		actual, err := f.runtime.classifyInstance(t.Context(), f.client, f.iid, f.labels)
		if err != nil || actual != class {
			t.Fatalf("legacy %s adoption: %s %v", class, actual, err)
		}
	}
	for _, hint := range []string{owned.LabelHealthChecker, owned.LabelCatalogKind, enginebackup.LabelRevision} {
		f := newMutationFixture(t, false)
		delete(f.volumes, instanceClassName(f.iid))
		source := f.containers[ContainerName(f.iid)]
		delete(f.labels, enginebackup.LabelEngine)
		delete(f.labels, enginebackup.LabelRevision)
		delete(source.Config.Labels, enginebackup.LabelEngine)
		delete(source.Config.Labels, enginebackup.LabelRevision)
		source.Config.Labels[hint] = map[string]string{owned.LabelHealthChecker: "duckdb-v1", owned.LabelCatalogKind: "duckdb", enginebackup.LabelRevision: "2"}[hint]
		if _, err := f.runtime.classifyInstance(t.Context(), f.client, f.iid, f.labels); err == nil {
			t.Fatal("stripped Duck metadata became ordinary")
		}
	}
}

func TestApplyTerminalFindsOldSourceOutsidePreviousNames(t *testing.T) {
	f := newMutationFixture(t, false)
	guard, err := f.runtime.acquireDuckMutation(t.Context(), f.client, f.iid, f.labels, duckMutationIntent{Action: "apply", Image: f.labels[owned.LabelImage], Revision: f.labels[owned.LabelRevision]})
	if err != nil {
		t.Fatal(err)
	}
	f.change(func() {
		original := f.containers[ContainerName(f.iid)]
		f.containers["unexpected-name"] = original
		replacement := *original
		replacement.ID = "replacement"
		f.containers[ContainerName(f.iid)] = &replacement
	})
	if err := guard.finish(t.Context(), nil); err == nil || !f.held() {
		t.Fatal("old source outside previous names permitted release")
	}
}

type mutationFenceAccess struct {
	HostAccess
	request func(context.Context, string, EffectFence, string, string, string, string, io.Reader, http.Header) (*http.Response, error)
}

func (a *mutationFenceAccess) FencedRequest(ctx context.Context, mode string, fence EffectFence, daemon, socket, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return a.request(ctx, mode, fence, daemon, socket, method, path, body, headers)
}
func (*mutationFenceAccess) Close() error { return nil }

func TestCheckpointDetachedCleanupPreservesDirtyEffectFence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := newMutationFixture(t, false)
		var dirty atomic.Bool
		expected := EffectFence{Operation: "org/backup/operation", Generation: 9}
		f.client.RequireFence = true
		f.client.Config.Details = map[string]string{"docker_id": "daemon"}
		f.client.access = &mutationFenceAccess{request: func(ctx context.Context, _ string, fence EffectFence, daemon, _ string, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
			if fence != expected || daemon != "daemon" || ctx.Err() != nil {
				return nil, errors.New("cleanup lost valid effect fence or detached context")
			}
			if dirty.Load() {
				return nil, errors.New("fence journal is dirty")
			}
			request, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
			if err != nil {
				return nil, err
			}
			request.Header = headers.Clone()
			return f.client.HTTP.Do(request)
		}}
		ctx, cancel := context.WithCancel(WithEffectFence(t.Context(), expected))
		err := f.runtime.stoppedEngine(ctx, f.client, f.iid, f.labels, "backup", "object", func(context.Context, string) error { cancel(); dirty.Store(fail); return context.Canceled })
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled copy lost original error: %v", err)
		}
		if f.held() != fail {
			t.Fatalf("dirty=%v, held=%v: %v", fail, f.held(), err)
		}
		exists, running := f.source()
		if !exists || running == fail {
			t.Fatal("detached cleanup ignored fence result")
		}
	}
}

// This follows the documented host procedure using the daemon's saved record,
// after the simulated worker has lost its in-memory guard.
func TestDuckWorkerCrashRequiresDrainedExactHostReconciliation(t *testing.T) {
	for _, mode := range []string{"recover", "workers-running", "previous", "source", "volume", "owner", "native-auth"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, false)
			if _, err := f.runtime.acquireDuckMutation(t.Context(), f.client, f.iid, f.labels, duckMutationIntent{Action: "backup", Operation: "object"}); err != nil {
				t.Fatal(err)
			}
			if err := dockerSetRunning(t.Context(), f.client, "source", false); err != nil {
				t.Fatal(err)
			}
			if err := f.runtime.Remove(t.Context(), f.iid, f.labels); err == nil {
				t.Fatal("new worker stole abandoned reservation")
			}
			reservationName := ContainerName(f.iid) + "-mutation"
			var saved mutationContainer
			if err := dockerJSON(t.Context(), f.client, http.MethodGet, "/containers/"+reservationName+"/json", nil, &saved); err != nil {
				t.Fatal(err)
			}
			var record duckMutationRecord
			if err := json.Unmarshal([]byte(saved.Config.Labels[duckMutationRecordLabel]), &record); err != nil {
				t.Fatal(err)
			}
			f.change(func() {
				switch mode {
				case "previous":
					f.containers[ContainerName(f.iid)+"-previous"] = &mutationContainer{containerInfo: containerInfo{ID: "unknown"}}
				case "source":
					f.containers[ContainerName(f.iid)].ID = "replacement"
				case "volume":
					v := f.volumes[record.Volume]
					v.CreatedAt = "replacement"
					f.volumes[v.Name] = v
				case "owner":
					f.containers[reservationName].Config.Labels[duckMutationRecordLabel] = "changed"
				case "native-auth":
					f.failProbe = true
				}
			})
			reconcile := func() error {
				if mode == "workers-running" {
					return errors.New("all workers must stop and drain")
				}
				var owner mutationContainer
				if err := dockerJSON(t.Context(), f.client, http.MethodGet, "/containers/"+saved.ID+"/json", nil, &owner); err != nil {
					return err
				}
				if owner.ID != saved.ID || owner.Name != saved.Name || owner.State.Running || len(owner.Mounts) != 0 || !maps.Equal(owner.Config.Labels, saved.Config.Labels) {
					return ErrOwned
				}

				if record.Owner == "" || record.Version != 1 || record.Action != "backup" || record.Operation != "object" || record.Org != "org" || record.Project != "project" || record.Environment != "env" || record.Connection != "target" || record.Instance != f.iid.String() || record.Resource != "analytics" {
					return ErrOwned
				}
				for _, suffix := range []string{"-previous", "-previous-stopped", "-checkpoint"} {
					var c containerInfo
					if err := dockerJSON(t.Context(), f.client, http.MethodGet, "/containers/"+ContainerName(f.iid)+suffix+"/json", nil, &c); !errors.Is(err, ErrMissing) {
						return ErrOwned
					}
				}
				current, err := f.runtime.inspect(t.Context(), f.client, ContainerName(f.iid), f.iid, f.labels)
				if err != nil {
					return err
				}
				if current.ID != record.SourceID || current.Config.Image != record.SourceImage || !maps.Equal(mutationSourceLabels(current.Config.Labels), record.SourceLabels) {
					return ErrOwned
				}
				volume, err := inspectDataVolume(t.Context(), f.client, f.iid, f.labels)
				if err != nil {
					return err
				}
				if volume == nil || volume.CreatedAt != record.VolumeCreated {
					return ErrOwned
				}
				if err := dockerSetRunning(t.Context(), f.client, record.SourceID, false); err != nil {
					return err
				}
				if err := dockerSetRunning(t.Context(), f.client, record.SourceID, true); err != nil {
					return err
				}
				if err := ExecOutput(t.Context(), f.client, record.SourceID, []string{"/loomplane-duckdb", "check"}, io.Discard); err != nil {
					return err
				}
				var final mutationContainer
				if err := dockerJSON(t.Context(), f.client, http.MethodGet, "/containers/"+saved.ID+"/json", nil, &final); err != nil {
					return err
				}
				if final.ID != saved.ID || final.Name != saved.Name || final.State.Running || len(final.Mounts) != 0 || !maps.Equal(final.Config.Labels, saved.Config.Labels) {
					return ErrOwned
				}
				return dockerJSON(t.Context(), f.client, http.MethodDelete, "/containers/"+saved.ID, nil, nil)
			}
			f.mu.Lock()
			beforeEffects, beforeProbes := f.sourceEffects, f.probes
			f.mu.Unlock()
			err := reconcile()
			f.mu.Lock()
			ownerEffects := f.sourceEffects != beforeEffects || f.probes != beforeProbes
			f.mu.Unlock()
			if mode == "owner" && ownerEffects {
				t.Fatal("changed reservation owner reached source stop/start/probe")
			}
			if (mode == "recover") != (err == nil) || f.held() != (mode != "recover") {
				t.Fatalf("host reconciliation: %v held=%v", err, f.held())
			}
		})
	}
}

func TestDuckApplyRemoveAndPurgeKeepPermanentClass(t *testing.T) {
	for _, private := range []bool{false, true} {
		f := newMutationFixture(t, private)
		clear(f.containers)
		clear(f.volumes)
		spec := cpprovider.ServiceSpec{Name: "main", Image: f.labels[owned.LabelImage], Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}
		if _, err := f.runtime.Apply(t.Context(), f.iid, spec, f.labels); err != nil {
			t.Fatal(err)
		}
		if f.held() {
			t.Fatal("successful first Apply held reservation")
		}
		var err error
		if private {
			err = f.runtime.PurgeRecovery(t.Context(), f.iid, f.labels)
		} else {
			err = f.runtime.Remove(t.Context(), f.iid, f.labels)
		}
		if err != nil {
			t.Fatal(err)
		}
		if exists, _ := f.source(); exists || f.held() {
			t.Fatal("removal did not settle")
		}
		f.mu.Lock()
		anchor := f.volumes[instanceClassName(f.iid)]
		_, dataRemains := f.volumes[VolumeName(f.iid, "data")]
		f.mu.Unlock()
		if anchor.Name == "" || dataRemains == private {
			t.Fatal("removal lost permanent classification or wrong storage")
		}
		ordinary := maps.Clone(f.labels)
		delete(ordinary, enginebackup.LabelEngine)
		delete(ordinary, enginebackup.LabelRevision)
		delete(ordinary, enginebackup.LabelSource)
		if _, err := f.runtime.Apply(t.Context(), f.iid, spec, ordinary); err == nil {
			t.Fatal("removed Duck instance changed class")
		}
	}
}

func TestCheckpointRefusesSourceStoppedBeforeClaim(t *testing.T) {
	for _, action := range []string{"backup", "restore"} {
		f := newMutationFixture(t, action == "restore")
		f.containers[ContainerName(f.iid)].State.Running = false
		called := false
		err := f.runtime.stoppedEngine(t.Context(), f.client, f.iid, f.labels, action, "operation", func(context.Context, string) error { called = true; return nil })
		if err == nil || called || f.held() || f.sourceEffects != 0 {
			t.Fatal("checkpoint restarted an already stopped source")
		}
	}
}

func TestDuckExistingLifecycleNeverFetchesRegistryImage(t *testing.T) {
	for _, action := range []string{"backup", "set-running", "remove"} {
		f := newMutationFixture(t, false)
		f.containers[ContainerName(f.iid)].Image = "sha256:" + strings.Repeat("b", 64)
		next := f.client.HTTP.Transport
		var imageRequests atomic.Int64
		f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
			if strings.HasPrefix(req.URL.Path, "/images/") {
				imageRequests.Add(1)
				return nil, errors.New("registry unavailable and repository reference removed")
			}
			return next.RoundTrip(req)
		})
		var err error
		switch action {
		case "backup":
			err = f.runtime.stoppedEngine(t.Context(), f.client, f.iid, f.labels, action, "object", func(context.Context, string) error { return nil })
		case "set-running":
			err = f.runtime.SetRunning(t.Context(), f.iid, f.labels, false)
		case "remove":
			err = f.runtime.Remove(t.Context(), f.iid, f.labels)
		}
		if err != nil || imageRequests.Load() != 0 {
			t.Fatalf("offline existing lifecycle: %v, image requests=%d", err, imageRequests.Load())
		}
	}
}

func TestDuckMixedMarkersCannotEnterOrdinaryNativeSessions(t *testing.T) {
	for _, kind := range []string{"postgres-direct", "mysql", "redis", "nats", "rabbitmq"} {
		for _, hint := range []string{"engine", "checker", "catalog"} {
			for _, preserve := range []bool{false, true} {
				t.Run(kind+"/"+hint+map[bool]string{false: "/stripped", true: "/preserved"}[preserve], func(t *testing.T) {
					f := newMutationFixture(t, kind == "postgres-direct")
					labels := maps.Clone(f.labels)
					source := f.containers[ContainerName(f.iid)]
					// Populate the ordinary positive marker as well as contradictory Duck data.
					switch kind {
					case "postgres-direct":
						labels[LabelDatabase] = "ordinarydb"
						source.Config.Labels[LabelDatabase] = "ordinarydb"
					case "mysql":
						labels[nativemysql.LabelDatabase] = "ordinarydb"
						labels[nativemysql.LabelEngine] = "mysql"
						source.Config.Labels[nativemysql.LabelDatabase] = "ordinarydb"
						source.Config.Labels[nativemysql.LabelEngine] = "mysql"
					case "redis":
						labels[nativecache.LabelEngine] = "redis"
						source.Config.Labels[nativecache.LabelEngine] = "redis"
					case "nats":
						labels[nativenats.LabelEngine] = "nats"
						source.Config.Labels[nativenats.LabelEngine] = "nats"
					case "rabbitmq":
						labels[nativerabbit.LabelEngine] = "rabbitmq"
						source.Config.Labels[nativerabbit.LabelEngine] = "rabbitmq"
					}
					if hint != "engine" {
						delete(source.Config.Labels, enginebackup.LabelEngine)
						if hint == "checker" {
							source.Config.Labels[owned.LabelHealthChecker] = "duckdb-v1"
						} else {
							source.Config.Labels[owned.LabelCatalogKind] = "duckdb"
						}
					}
					if !preserve {
						delete(labels, enginebackup.LabelEngine)
					}
					source.Config.Env = []string{"LOOMPLANE_APP_PASSWORD=valid-native-password-at-least-26-characters", "LOOMPLANE_NATS_ADMIN_PASSWORD=valid-native-password-at-least-26-characters"}
					source.NetworkSettings.Networks = map[string]struct{ IPAddress, NetworkID string }{runtimeNetwork(labels): {IPAddress: "127.0.0.1", NetworkID: "network"}}
					var effects atomic.Int64
					next := f.client.HTTP.Transport
					f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
						if req.Method != http.MethodGet && req.Method != http.MethodHead {
							effects.Add(1)
						}
						return next.RoundTrip(req)
					})
					adapterEntered := false
					nativeAccess := &ordinaryNativeAccess{}
					if kind == "nats" {
						f.client.access = nativeAccess
					}
					var err error
					switch kind {
					case "postgres-direct":
						err = f.runtime.RestorePostgres(t.Context(), f.iid, labels, nil, backupfiles.Object{})
					case "mysql":
						err = f.runtime.ConfigureMySQL(t.Context(), f.iid, labels, "valid-native-password-at-least-26-characters")
					case "redis":
						err = f.runtime.configureCache(t.Context(), f.iid, labels, "valid-native-password-at-least-26-characters")
					case "nats":
						_, err = f.runtime.natsDatabase(f.client, f.iid, labels, "").Connect(t.Context(), false)
					case "rabbitmq":
						err = f.runtime.withRabbitMQ(t.Context(), f.iid, labels, "", func(db nativerabbit.Database) error { adapterEntered = true; return db.Probe(t.Context()) })
					}
					if !errors.Is(err, ErrOwned) || effects.Load() != 0 || adapterEntered || nativeAccess.dials.Load() != 0 {
						t.Fatalf("ordinary session admitted Duck source: %v effects=%d adapter=%v dials=%d", err, effects.Load(), adapterEntered, nativeAccess.dials.Load())
					}
				})
			}
		}
	}
}

type ordinaryNativeAccess struct {
	HostAccess
	dials atomic.Int64
}

func (a *ordinaryNativeAccess) DialContext(context.Context, string, string) (net.Conn, error) {
	a.dials.Add(1)
	return nil, errors.New("native connection fixture reached")
}
func (*ordinaryNativeAccess) Close() error { return nil }

func TestOrdinaryNativeSessionsRetainTheirExistingAdmission(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "redis", "nats", "rabbitmq"} {
		t.Run(kind, func(t *testing.T) {
			f := newMutationFixture(t, false)
			labels := maps.Clone(f.labels)
			source := f.containers[ContainerName(f.iid)]
			for _, key := range []string{enginebackup.LabelEngine, enginebackup.LabelRevision, enginebackup.LabelSource} {
				delete(labels, key)
				delete(source.Config.Labels, key)
			}
			switch kind {
			case "postgres":
				labels[LabelDatabase] = "ordinarydb"
				source.Config.Labels[LabelDatabase] = "ordinarydb"
			case "mysql":
				labels[nativemysql.LabelDatabase] = "ordinarydb"
				labels[nativemysql.LabelEngine] = "mysql"
				source.Config.Labels[nativemysql.LabelDatabase] = "ordinarydb"
				source.Config.Labels[nativemysql.LabelEngine] = "mysql"
			case "redis":
				labels[nativecache.LabelEngine] = "redis"
				source.Config.Labels[nativecache.LabelEngine] = "redis"
			case "nats":
				labels[nativenats.LabelEngine] = "nats"
				source.Config.Labels[nativenats.LabelEngine] = "nats"
			case "rabbitmq":
				labels[nativerabbit.LabelEngine] = "rabbitmq"
				source.Config.Labels[nativerabbit.LabelEngine] = "rabbitmq"
			}
			source.Config.Env = []string{"LOOMPLANE_APP_PASSWORD=valid-native-password-at-least-26-characters", "LOOMPLANE_NATS_ADMIN_PASSWORD=valid-native-password-at-least-26-characters"}
			source.NetworkSettings.Networks = map[string]struct{ IPAddress, NetworkID string }{runtimeNetwork(labels): {IPAddress: "127.0.0.1", NetworkID: "network"}}
			var err error
			switch kind {
			case "postgres":
				_, err = f.runtime.postgresSession(t.Context(), f.iid, labels)
			case "mysql":
				_, err = f.runtime.mysqlSession(t.Context(), f.iid, labels)
			case "redis":
				_, err = f.runtime.cacheSession(t.Context(), f.iid, labels)
			case "nats":
				access := &ordinaryNativeAccess{}
				f.client.access = access
				_, err = f.runtime.natsDatabase(f.client, f.iid, labels, "").Connect(t.Context(), false)
				if access.dials.Load() != 1 || errors.Is(err, ErrOwned) {
					t.Fatalf("ordinary NATS did not reach native connection: %v dials=%d", err, access.dials.Load())
				}
				return
			case "rabbitmq":
				entered := false
				err = f.runtime.withRabbitMQ(t.Context(), f.iid, labels, "", func(nativerabbit.Database) error { entered = true; return nil })
				if !entered {
					t.Fatal("ordinary RabbitMQ adapter was not admitted")
				}
			}
			if err != nil {
				t.Fatalf("ordinary session changed behavior: %v", err)
			}
		})
	}
}

func TestDuckOrphanNativeMetadataRefusesAdoptionAndOrdinaryRestore(t *testing.T) {
	for _, mode := range []string{"source-only", "malformed-orphan", "orphan-revision", "duck-source-other-engine", "malformed-known-source", "wrong-source-revision"} {
		for _, preserve := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/stripped", true: "/preserved"}[preserve], func(t *testing.T) {
				f := newMutationFixture(t, true)
				delete(f.volumes, instanceClassName(f.iid))
				desired := maps.Clone(f.labels)
				desired[LabelDatabase] = "ordinarydb"
				actual := f.containers[ContainerName(f.iid)].Config.Labels
				actual[LabelDatabase] = "ordinarydb"
				original := actual[enginebackup.LabelSource]
				for _, key := range []string{enginebackup.LabelEngine, enginebackup.LabelRevision, owned.LabelHealthChecker, owned.LabelCatalogKind} {
					delete(actual, key)
				}
				switch mode {
				case "malformed-orphan":
					actual[enginebackup.LabelSource] = "{bad-json"
				case "orphan-revision":
					delete(actual, enginebackup.LabelSource)
					actual[enginebackup.LabelRevision] = "2"
				case "duck-source-other-engine":
					actual[enginebackup.LabelEngine] = "falkordb"
					actual[enginebackup.LabelRevision] = "2"
				case "malformed-known-source":
					actual[enginebackup.LabelEngine] = "falkordb"
					actual[enginebackup.LabelRevision] = "2"
					actual[enginebackup.LabelSource] = "{}"
				case "wrong-source-revision":
					var identity enginebackup.Identity
					if err := json.Unmarshal([]byte(original), &identity); err != nil {
						t.Fatal(err)
					}
					identity.Kind = "falkordb"
					identity.Format = enginebackup.Format("falkordb")
					identity.Revision = "other"
					encoded, err := json.Marshal(identity)
					if err != nil {
						t.Fatal(err)
					}
					actual[enginebackup.LabelEngine] = "falkordb"
					actual[enginebackup.LabelRevision] = "2"
					actual[enginebackup.LabelSource] = string(encoded)
				}
				if !preserve {
					for _, key := range []string{enginebackup.LabelEngine, enginebackup.LabelRevision, enginebackup.LabelSource} {
						delete(desired, key)
					}
				}
				if _, err := f.runtime.classifyInstance(t.Context(), f.client, f.iid, desired); err == nil {
					t.Error("ambiguous native metadata adopted ordinary class")
				}
				f.mu.Lock()
				_, anchored := f.volumes[instanceClassName(f.iid)]
				f.mu.Unlock()
				if anchored {
					t.Error("ambiguous metadata wrote an anchor")
				}
				var effects atomic.Int64
				next := f.client.HTTP.Transport
				f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method != http.MethodGet && req.Method != http.MethodHead {
						effects.Add(1)
					}
					return next.RoundTrip(req)
				})
				err := f.runtime.RestorePostgres(t.Context(), f.iid, desired, nil, backupfiles.Object{})
				if !errors.Is(err, ErrOwned) || effects.Load() != 0 {
					t.Errorf("ambiguous source reached ordinary restore: error=%v effects=%d", err, effects.Load())
				}
			})
		}
	}
}

func TestKnownOrdinaryRecoveryIdentityRemainsAdmissible(t *testing.T) {
	for _, kind := range []string{"falkordb", "elasticsearch", "opensearch"} {
		f := newMutationFixture(t, true)
		delete(f.volumes, instanceClassName(f.iid))
		var original enginebackup.Identity
		if err := json.Unmarshal([]byte(f.labels[enginebackup.LabelSource]), &original); err != nil {
			t.Fatal(err)
		}
		original.Kind = kind
		original.Format = enginebackup.Format(kind)
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		f.labels[enginebackup.LabelEngine] = kind
		f.labels[enginebackup.LabelSource] = string(encoded)
		f.containers[ContainerName(f.iid)].Config.Labels = owned.InstanceLabels(f.labels, f.iid)
		class, err := f.runtime.classifyInstance(t.Context(), f.client, f.iid, f.labels)
		if err != nil || class != "ordinary" {
			t.Fatalf("known ordinary recovery identity refused: %s %v", class, err)
		}
		if _, err := f.runtime.ordinaryEngineSource(t.Context(), f.client, f.iid, f.labels); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDuckFirstApplyUsesRegistryAuthenticationOnce(t *testing.T) {
	f := newMutationFixture(t, false)
	clear(f.containers)
	clear(f.volumes)
	const auth = "synthetic-private-registry-auth"
	var valuesCalls, pulls atomic.Int64
	var leaked atomic.Bool
	f.onClaim = func(f *mutationFixture) {
		for _, value := range f.containers[ContainerName(f.iid)+"-mutation"].Config.Labels {
			if strings.Contains(value, auth) {
				leaked.Store(true)
			}
		}
	}
	var cached atomic.Bool
	f.runtime.Values = func(_ context.Context, labels map[string]string) (owned.RuntimeValues, error) {
		valuesCalls.Add(1)
		f.mu.Lock()
		anchor := f.volumes[instanceClassName(f.iid)]
		f.mu.Unlock()
		if anchor.Name == "" || labels[owned.LabelInstance] != f.iid.String() {
			return owned.RuntimeValues{}, errors.New("values resolved before classification or without exact instance")
		}
		return owned.RuntimeValues{PullAuth: auth}, nil
	}
	next := f.client.HTTP.Transport
	f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/images/create" {
			pulls.Add(1)
			if req.Header.Get("X-Registry-Auth") != auth {
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("authentication required")), Header: http.Header{}}, nil
			}
			cached.Store(true)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}\n")), Header: http.Header{}}, nil
		}
		if strings.HasPrefix(req.URL.Path, "/images/") && req.Method == http.MethodGet && !cached.Load() {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("image absent")), Header: http.Header{}}, nil
		}
		if req.URL.Path == "/containers/create" && !cached.Load() {
			return nil, errors.New("reservation created before authenticated image acquisition")
		}
		return next.RoundTrip(req)
	})
	spec := cpprovider.ServiceSpec{Name: "main", Image: f.labels[owned.LabelImage], Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}
	_, err := f.runtime.Apply(t.Context(), f.iid, spec, f.labels)
	if err != nil {
		t.Fatalf("authenticated first Apply failed: %v", err)
	}
	if leaked.Load() {
		t.Fatal("registry auth persisted in reservation record")
	}
	if valuesCalls.Load() != 1 || pulls.Load() != 1 {
		t.Fatalf("values resolutions=%d pulls=%d", valuesCalls.Load(), pulls.Load())
	}
	if f.held() {
		t.Fatal("successful authenticated Apply retained reservation")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, volume := range f.volumes {
		for _, value := range volume.Labels {
			if strings.Contains(value, auth) {
				t.Fatal("registry auth persisted in volume labels")
			}
		}
	}
	for _, container := range f.containers {
		for _, value := range container.Config.Labels {
			if strings.Contains(value, auth) {
				t.Fatal("registry auth persisted in container labels")
			}
		}
	}
}
