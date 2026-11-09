package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

func qualifyDuckFixture(t *testing.T, f *mutationFixture) {
	t.Helper()
	f.labels[enginebackup.LabelRevision] = "3"
	var original enginebackup.Identity
	if err := json.Unmarshal([]byte(f.labels[enginebackup.LabelSource]), &original); err != nil {
		t.Fatal(err)
	}
	original.Revision = "3"
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	f.labels[enginebackup.LabelSource] = string(raw)
	c := f.containers[ContainerName(f.iid)]
	c.Config.Labels = owned.InstanceLabels(f.labels, f.iid)
	c.Config.Entrypoint = []string{"/loomplane-duckdb"}
	c.Config.Cmd = []string{"serve-readonly"}
	c.Image = "sha256:" + strings.Repeat("b", 64)
	ro := false
	c.Mounts[0].RW = &ro
}

func TestDuckRecoveryServingRejectsMissingWritableAndAliasedMounts(t *testing.T) {
	for _, mode := range []string{"valid", "missing-rw", "writable", "nested", "alias", "old-revision", "image-mismatch", "wrong-command", "privileged", "capability"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, true)
			qualifyDuckFixture(t, f)
			c := &f.containers[ContainerName(f.iid)].containerInfo
			switch mode {
			case "missing-rw":
				c.Mounts[0].RW = nil
			case "writable":
				rw := true
				c.Mounts[0].RW = &rw
			case "nested":
				c.Mounts = append(c.Mounts, containerMount{Type: "tmpfs", Destination: "/data/nested"})
			case "alias":
				c.Mounts = append(c.Mounts, containerMount{Type: "volume", Name: VolumeName(f.iid, "data"), Destination: "/elsewhere"})
			case "old-revision":
				f.labels[enginebackup.LabelRevision] = "2"
			case "image-mismatch":
				c.Config.Image = "other/image@sha256:" + strings.Repeat("a", 64)
			case "wrong-command":
				c.Config.Cmd = nil
			case "privileged":
				c.HostConfig.Privileged = true
			case "capability":
				c.HostConfig.CapAdd = []string{"SYS_ADMIN"}
			}
			err := validateDuckRecoveryServingMount(f.iid, c, f.labels)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if err := duckDataMount(c, f.iid); mode == "writable" && err != nil {
				t.Fatal("serving policy blocked legacy cleanup identity")
			}
		})
	}
}

func TestDuckStageUnknownOutcomeNeverDiscardsOrReleases(t *testing.T) {
	f := newMutationFixture(t, true)
	g, err := f.runtime.acquireDuckMutation(t.Context(), f.client, f.iid, f.labels, duckMutationIntent{Action: "purge"})
	if err != nil {
		t.Fatal(err)
	}
	g.record.Action = "restore"
	g.stageUnknown = true
	var effects atomic.Int64
	next := f.client.HTTP.Transport
	f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			effects.Add(1)
		}
		return next.RoundTrip(req)
	})
	for _, op := range []func(context.Context) error{g.discardRestore, g.release, func(ctx context.Context) error { return g.finish(ctx, nil) }} {
		if err := op(t.Context()); err == nil {
			t.Fatal("unknown stage outcome lost its reservation hold")
		}
	}
	if effects.Load() != 0 || !f.held() {
		t.Fatalf("unknown outcome caused effects=%d held=%t", effects.Load(), f.held())
	}
}

func TestDuckLegacyRecoveryStartRefusedBeforeSourceEffects(t *testing.T) {
	for _, op := range []string{"start", "apply", "probe"} {
		t.Run(op, func(t *testing.T) {
			f := newMutationFixture(t, true)
			f.labels[enginebackup.LabelRevision] = "2"
			var old enginebackup.Identity
			if err := json.Unmarshal([]byte(f.labels[enginebackup.LabelSource]), &old); err != nil {
				t.Fatal(err)
			}
			old.Revision = "2"
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			f.labels[enginebackup.LabelSource] = string(raw)
			c := f.containers[ContainerName(f.iid)]
			c.Config.Labels = owned.InstanceLabels(f.labels, f.iid)
			c.Mounts[0].RW = boolPointer(true)
			switch op {
			case "start":
				err = f.runtime.SetRunning(t.Context(), f.iid, f.labels, true)
			case "apply":
				_, err = f.runtime.Apply(t.Context(), f.iid, cpprovider.ServiceSpec{Name: "main", Image: f.labels[owned.LabelImage], Command: []string{"/loomplane-duckdb"}, Args: []string{"serve-readonly"}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}, f.labels)
			case "probe":
				err = f.runtime.engineProbe(t.Context(), f.iid, f.labels)
			}
			if err == nil || f.sourceEffects != 0 || f.starts != 0 || f.probes != 0 {
				t.Fatalf("legacy %s error=%v effects=%d starts=%d probes=%d", op, err, f.sourceEffects, f.starts, f.probes)
			}
		})
	}
}

func TestDuckLegacyRecoveryCleanupRemainsAvailable(t *testing.T) {
	for _, action := range []string{"stop", "remove", "purge", "status"} {
		t.Run(action, func(t *testing.T) {
			f := newMutationFixture(t, true)
			var original enginebackup.Identity
			if err := json.Unmarshal([]byte(f.labels[enginebackup.LabelSource]), &original); err != nil {
				t.Fatal(err)
			}
			original.Revision = "2"
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			f.labels[enginebackup.LabelRevision], f.labels[enginebackup.LabelSource] = "2", string(raw)
			c := f.containers[ContainerName(f.iid)]
			c.Config.Labels = owned.InstanceLabels(f.labels, f.iid)
			c.Mounts[0].RW = boolPointer(true)
			switch action {
			case "stop":
				err = f.runtime.SetRunning(t.Context(), f.iid, f.labels, false)
			case "remove":
				err = f.runtime.Remove(t.Context(), f.iid, f.labels)
			case "purge":
				err = f.runtime.PurgeRecovery(t.Context(), f.iid, f.labels)
			case "status":
				status, statusErr := f.runtime.Status(t.Context(), f.iid, f.labels)
				err = statusErr
				if statusErr == nil && (status.Ready || f.probes != 0) {
					t.Fatal("legacy status claimed verified readiness")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if action != "status" && f.held() {
				t.Fatal("legacy cleanup retained a clean reservation")
			}
		})
	}
}

func TestDuckStageCensusRejectsRenamedAndUnknownAttachment(t *testing.T) {
	for _, mode := range []string{"renamed-stage", "unexpected-rw", "missing-mounts", "inspect-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, true)
			qualifyDuckFixture(t, f)
			next := f.client.HTTP.Transport
			f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/containers/json" {
					mounts := []containerMount{{Type: "volume", Name: VolumeName(f.iid, "data"), Destination: "/data"}}
					labels := map[string]string{}
					if mode == "renamed-stage" {
						labels[duckStageLabel] = f.iid.String()
						mounts = nil
					}
					item := map[string]any{"Id": "foreign", "Labels": labels, "Mounts": mounts}
					if mode == "missing-mounts" {
						delete(item, "Mounts")
					}
					raw, err := json.Marshal([]any{item})
					if err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
				}
				return next.RoundTrip(req)
			})
			if err := duckStageCensus(t.Context(), f.client, f.iid, "source", "", false); err == nil {
				t.Fatal("unexpected attachment inventory accepted")
			}
		})
	}
}

func TestDuckQualifiedRecoveryIdentityBindsOriginalImage(t *testing.T) {
	f := newMutationFixture(t, true)
	qualifyDuckFixture(t, f)
	if err := validateDuckRecoveryContract(f.iid, f.labels); err != nil {
		t.Fatal(err)
	}
	for _, change := range []map[string]string{{enginebackup.LabelRevision: "4"}, {owned.LabelImage: "local/unreviewed@sha256:" + strings.Repeat("c", 64)}, {enginebackup.LabelSource: "{}"}} {
		labels := maps.Clone(f.labels)
		maps.Copy(labels, change)
		if err := validateDuckRecoveryContract(f.iid, labels); !errors.Is(err, ErrOwned) {
			t.Fatalf("changed original identity admitted: %v", err)
		}
	}
}

func boolPointer(value bool) *bool { return &value }

func TestDuckCensusNestedMountPolicyPreservesOrdinaryAndLegacyCleanup(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-source", true: "legacy-recovery"}[private], func(t *testing.T) {
			f := newMutationFixture(t, private)
			c := f.containers[ContainerName(f.iid)]
			c.Mounts[0].RW = boolPointer(true)
			c.Mounts = append(c.Mounts, containerMount{Type: "tmpfs", Destination: "/data/scratch"})
			if err := duckStageCensus(t.Context(), f.client, f.iid, c.ID, "", false); err != nil {
				t.Fatalf("cleanup rejected an unrelated nested mount: %v", err)
			}
			c.Mounts[0].RW = boolPointer(false)
			if err := duckStageCensus(t.Context(), f.client, f.iid, c.ID, "", true); err == nil {
				t.Fatal("recovery serving accepted a nested data mount")
			}
			c.Mounts = c.Mounts[:1]
			c.Mounts = append(c.Mounts, containerMount{Type: "volume", Name: VolumeName(f.iid, "data"), Destination: "/aliased-data", RW: boolPointer(true)})
			for _, requireRO := range []bool{false, true} {
				if err := duckStageCensus(t.Context(), f.client, f.iid, c.ID, "", requireRO); err == nil {
					t.Fatal("data-volume alias was accepted")
				}
			}
		})
	}
}

func TestDuckRecoveryStoppedApplyRefusesBeforeMutationAndStillStarts(t *testing.T) {
	f := newMutationFixture(t, true)
	c := f.containers[ContainerName(f.iid)]
	c.Config.Env = []string{"LOOMPLANE_APP_PASSWORD=synthetic-unchanged-token"}
	if err := f.runtime.SetRunning(t.Context(), f.iid, f.labels, false); err != nil {
		t.Fatal(err)
	}
	beforeID, beforeLabels, beforeEnv := c.ID, maps.Clone(c.Config.Labels), append([]string(nil), c.Config.Env...)
	beforeClaims := f.claims
	var effects atomic.Int64
	next := f.client.HTTP.Transport
	f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			effects.Add(1)
		}
		return next.RoundTrip(req)
	})
	spec := cpprovider.ServiceSpec{Name: "main", Image: f.labels[owned.LabelImage], Command: []string{"/loomplane-duckdb"}, Args: []string{"serve-readonly"}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}
	if _, err := f.runtime.Apply(t.Context(), f.iid, spec, f.labels); err == nil {
		t.Fatal("stopped immutable recovery Apply was accepted")
	}
	c = f.containers[ContainerName(f.iid)]
	if effects.Load() != 0 || f.claims != beforeClaims || f.held() || c == nil || c.ID != beforeID || c.State.Running || !maps.Equal(c.Config.Labels, beforeLabels) || !slices.Equal(c.Config.Env, beforeEnv) {
		t.Fatalf("refused Apply changed stopped copy: effects=%d claims=%d held=%t", effects.Load(), f.claims-beforeClaims, f.held())
	}
	if err := f.runtime.SetRunning(t.Context(), f.iid, f.labels, true); err != nil {
		t.Fatalf("authorized same-copy restart failed after Apply refusal: %v", err)
	}
	c = f.containers[ContainerName(f.iid)]
	if c.ID != beforeID || !c.State.Running || !maps.Equal(c.Config.Labels, beforeLabels) || !slices.Equal(c.Config.Env, beforeEnv) || f.held() {
		t.Fatal("restart replaced identity, credentials or reservation state")
	}
}

func TestDuckRecoveryReplacementApplyRefusesBeforeReservation(t *testing.T) {
	f := newMutationFixture(t, true)
	desired := maps.Clone(f.labels)
	desired[owned.LabelRevision] = "unsupported-replacement"
	var effects atomic.Int64
	next := f.client.HTTP.Transport
	f.client.HTTP.Transport = runtimeTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			effects.Add(1)
		}
		return next.RoundTrip(req)
	})
	spec := cpprovider.ServiceSpec{Name: "main", Image: desired[owned.LabelImage], Command: []string{"/loomplane-duckdb"}, Args: []string{"serve-readonly"}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}
	if _, err := f.runtime.Apply(t.Context(), f.iid, spec, desired); err == nil {
		t.Fatal("replacement recovery Apply was accepted")
	}
	c := f.containers[ContainerName(f.iid)]
	if effects.Load() != 0 || f.held() || c.ID != "source" || !c.State.Running || c.Config.Labels[owned.LabelRevision] != f.labels[owned.LabelRevision] {
		t.Fatal("replacement refusal changed current copy")
	}
	c.State.Running = false
	if _, err := f.runtime.acquireDuckMutation(t.Context(), f.client, f.iid, f.labels, duckMutationIntent{Action: "apply", Image: f.labels[owned.LabelImage], Revision: f.labels[owned.LabelRevision]}); err == nil {
		t.Fatal("later stopped observation acquired an Apply reservation")
	}
	if effects.Load() != 0 || f.held() {
		t.Fatal("stopped pre-claim observation mutated recovery")
	}
}

type duckStageFixture struct {
	f            *mutationFixture
	next         http.RoundTripper
	data         []byte
	fail         string
	events       []string
	uploadTarget string
}

func installDuckStageFixture(t *testing.T, f *mutationFixture) *duckStageFixture {
	t.Helper()
	h := &duckStageFixture{f: f, next: f.client.HTTP.Transport, data: []byte("00000000DUCKsynthetic-checkpoint")}
	f.client.HTTP.Transport = runtimeTransport(h.roundTrip)
	return h
}

func stageResponse(code int, raw []byte) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}
}

func (h *duckStageFixture) roundTrip(req *http.Request) (*http.Response, error) {
	f := h.f
	h.events = append(h.events, req.Method+" "+req.URL.Path)
	stageName := ContainerName(f.iid) + "-readonly-stage"
	if req.URL.Path == "/containers/create" && req.URL.Query().Get("name") == stageName {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var body struct {
			Image           string
			Entrypoint, Cmd []string
			HostConfig      json.RawMessage
			Labels          map[string]string
		}
		if decodeErr := json.Unmarshal(raw, &body); decodeErr != nil {
			return nil, decodeErr
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		response, err := h.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		f.mu.Lock()
		c := f.containers[stageName]
		c.ID, c.Image = "stage", body.Image
		c.Config.Entrypoint, c.Config.Cmd = body.Entrypoint, body.Cmd
		err = json.Unmarshal(body.HostConfig, &c.HostConfig)
		f.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if h.fail == "create-lost" {
			return nil, errors.New("synthetic lost create response")
		}
		return stageResponse(http.StatusCreated, []byte(`{"Id":"stage"}`)), nil
	}
	if req.URL.Path == "/containers/stage/json" && h.fail == "create-readback" {
		return nil, errors.New("synthetic readback unavailable")
	}
	if req.URL.Path == "/containers/stage/start" {
		response, err := h.next.RoundTrip(req)
		if h.fail == "start-lost" && err == nil {
			response.Body.Close()
			return nil, errors.New("synthetic lost start response")
		}
		return response, err
	}
	if req.URL.Path == "/containers/stage/wait" {
		if h.fail == "wait-lost" {
			return nil, context.DeadlineExceeded
		}
		exit := 0
		if h.fail == "init-exit" {
			exit = 7
		}
		f.change(func() {
			c := f.containers[stageName]
			c.State.Running, c.State.Status, c.State.ExitCode = false, "exited", exit
		})
		h.data = []byte("00000000DUCKinitialized-fixture")
		encoded, err := json.Marshal(map[string]int{"StatusCode": exit})
		if err != nil {
			return nil, err
		}
		return stageResponse(http.StatusOK, encoded), nil
	}
	if req.URL.Path == "/containers/stage/archive" {
		if req.Method == http.MethodPut {
			h.uploadTarget = "stage"
			tr := tar.NewReader(req.Body)
			header, err := tr.Next()
			if err != nil || header.Name != "analytics.db" || header.Typeflag != tar.TypeReg {
				return nil, errors.New("unexpected import tar")
			}
			if h.fail == "upload-partial" {
				return nil, io.ErrUnexpectedEOF
			}
			h.data, err = io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			if _, err = tr.Next(); !errors.Is(err, io.EOF) {
				return nil, errors.New("extra import tar member")
			}
			switch h.fail {
			case "upload-lost":
				return nil, errors.New("synthetic lost archive response")
			case "upload-cancelled":
				return nil, context.Canceled
			case "upload-status":
				return stageResponse(http.StatusInternalServerError, nil), nil
			case "readback-mismatch":
				h.data = []byte("00000000DUCKdifferent-fixture")
			case "changed-volume":
				f.change(func() { v := f.volumes[VolumeName(f.iid, "data")]; v.CreatedAt = "replacement"; f.volumes[v.Name] = v })
			case "changed-owner":
				f.change(func() {
					f.containers[ContainerName(f.iid)+"-mutation"].Config.Labels[duckMutationRecordLabel] = "replacement"
				})
			case "renamed-helper":
				f.change(func() {
					c := f.containers[stageName]
					delete(f.containers, stageName)
					c.Name = "/renamed-stage"
					f.containers["renamed-stage"] = c
				})
			}
			return stageResponse(http.StatusOK, nil), nil
		}
		if req.Method == http.MethodGet {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			var err error
			if req.URL.Query().Get("path") == "/data" {
				err = tw.WriteHeader(&tar.Header{Name: "data/", Typeflag: tar.TypeDir, Mode: 0o755})
				if h.fail == "nonempty-new-volume" && err == nil {
					err = tw.WriteHeader(&tar.Header{Name: "data/unexpected", Typeflag: tar.TypeReg, Size: 0})
				}
			} else {
				err = tw.WriteHeader(&tar.Header{Name: "analytics.db", Typeflag: tar.TypeReg, Size: int64(len(h.data)), Mode: 0o600})
				if err == nil {
					_, err = tw.Write(h.data)
				}
			}
			if err == nil {
				err = tw.Close()
			}
			if err != nil {
				return nil, err
			}
			return stageResponse(http.StatusOK, raw.Bytes()), nil
		}
	}
	if req.URL.Path == "/containers/stage" && req.Method == http.MethodDelete {
		response, err := h.next.RoundTrip(req)
		if h.fail == "delete-lost" && err == nil {
			response.Body.Close()
			return nil, errors.New("synthetic lost delete response")
		}
		return response, err
	}
	if strings.HasSuffix(req.URL.Path, "/start") && req.URL.Path != "/containers/stage/start" {
		f.mu.Lock()
		hasHelper := false
		for _, c := range f.containers {
			hasHelper = hasHelper || c.Config.Labels[duckStageLabel] == f.iid.String()
		}
		f.mu.Unlock()
		if hasHelper {
			return nil, errors.New("serving start attempted while helper remained")
		}
	}
	return h.next.RoundTrip(req)
}

func TestDuckFreshRecoveryInitializesThenRemovesHelperBeforeServing(t *testing.T) {
	f := newMutationFixture(t, true)
	h := installDuckStageFixture(t, f)
	clear(f.containers)
	clear(f.volumes)
	h.data = nil
	spec := cpprovider.ServiceSpec{Name: "main", Image: f.labels[owned.LabelImage], Command: []string{"/loomplane-duckdb"}, Args: []string{"serve-readonly"}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}
	if _, err := f.runtime.Apply(t.Context(), f.iid, spec, f.labels); err != nil {
		t.Fatal(err)
	}
	if f.held() || f.containers[ContainerName(f.iid)+"-readonly-stage"] != nil {
		t.Fatal("successful serving retained helper or reservation")
	}
	c := f.containers[ContainerName(f.iid)]
	if !c.State.Running || c.Mounts[0].RW == nil || *c.Mounts[0].RW {
		t.Fatal("final clone was not running with read-only data")
	}
}

func TestDuckFreshRecoveryFaultNeverStartsOrReleasesCandidate(t *testing.T) {
	for _, mode := range []string{"create-lost", "create-readback", "nonempty-new-volume", "start-lost", "wait-lost", "init-exit", "delete-lost"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, true)
			h := installDuckStageFixture(t, f)
			h.fail = mode
			clear(f.containers)
			clear(f.volumes)
			spec := cpprovider.ServiceSpec{Name: "main", Image: f.labels[owned.LabelImage], Command: []string{"/loomplane-duckdb"}, Args: []string{"serve-readonly"}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: "/data"}}}
			if _, err := f.runtime.Apply(t.Context(), f.iid, spec, f.labels); err == nil {
				t.Fatal("failed bootstrap accepted")
			}
			c := f.containers[ContainerName(f.iid)]
			if c == nil || c.State.Running || !f.held() {
				t.Fatal("bootstrap failure removed, started or released candidate")
			}
			for _, event := range h.events {
				if event == "POST /containers/candidate/start" {
					t.Fatal("bootstrap failure attempted serving start")
				}
			}
		})
	}
}

func TestDuckImportUsesStoppedHelperAndPreservesServingIdentity(t *testing.T) {
	for _, mode := range []string{"success", "upload-lost", "upload-partial", "upload-status", "upload-cancelled", "readback-mismatch", "delete-lost", "changed-volume", "changed-owner", "renamed-helper"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationFixture(t, true)
			h := installDuckStageFixture(t, f)
			h.fail = mode
			payload, err := os.CreateTemp(t.TempDir(), "checkpoint")
			if err != nil {
				t.Fatal(err)
			}
			defer payload.Close()
			if _, writeErr := payload.Write([]byte("00000000DUCKverified-import")); writeErr != nil {
				t.Fatal(writeErr)
			}
			before := maps.Clone(f.containers[ContainerName(f.iid)].Config.Labels)
			err = f.runtime.stoppedEngineGuard(t.Context(), f.client, f.iid, f.labels, "restore", "operation", func(ctx context.Context, source string, g *duckMutation) error {
				return g.importRecovery(ctx, source, payload)
			})
			if h.uploadTarget != "stage" {
				t.Fatal("verified archive was not sent only to helper ID")
			}
			for _, event := range h.events {
				if event == "POST /containers/stage/start" {
					t.Fatal("import helper was started")
				}
			}
			if mode == "success" {
				c := f.containers[ContainerName(f.iid)]
				if err != nil || c == nil || c.ID != "source" || !c.State.Running || !maps.Equal(c.Config.Labels, before) || f.held() || f.containers[ContainerName(f.iid)+"-readonly-stage"] != nil {
					t.Fatalf("same clone import failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed stage accepted")
			}
			if mode == "readback-mismatch" {
				if exists, _ := f.source(); exists || f.held() {
					t.Fatal("positively completed corrupt import did not safely discard")
				}
				return
			}
			if exists, running := f.source(); !exists || running || !f.held() {
				t.Fatal("unknown import lost its stopped clone hold")
			}
			for _, event := range h.events {
				if event == "POST /containers/source/start" || event == "DELETE /containers/source" || event == "DELETE /containers/reservation" {
					t.Fatal("unknown import restarted, discarded or released")
				}
			}
		})
	}
}
