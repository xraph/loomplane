package server

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/recipes"
)

const duckStageLabel = "loomplane.duckdb-readonly-stage"
const duckStageRecordLabel = "loomplane.duckdb-readonly-stage.record"

func duckRecovery(labels map[string]string) bool {
	return labels[enginebackup.LabelEngine] == "duckdb" && labels[LabelRecovery] != ""
}

func validateDuckRecoveryContract(iid id.ID, labels map[string]string) error {
	if !duckRecovery(labels) || labels[owned.LabelPublic] != "false" || labels[enginebackup.LabelRevision] != recipes.DuckDBRecoveryRevision {
		return ErrOwned
	}
	identity, err := engineIdentity(iid, labels)
	if err != nil || identity.Instance == iid.String() || identity.Revision != recipes.DuckDBRecoveryRevision || identity.Image != labels[owned.LabelImage] {
		return ErrOwned
	}
	return nil
}

func validateDuckRecoverySpec(iid id.ID, spec cpprovider.ServiceSpec, labels map[string]string) error {
	if err := validateDuckRecoveryContract(iid, labels); err != nil {
		return err
	}
	if spec.Image != labels[owned.LabelImage] || !slices.Equal(spec.Command, []string{"/loomplane-duckdb"}) || !slices.Equal(spec.Args, []string{"serve-readonly"}) || len(spec.Volumes) != 1 || spec.Volumes[0].Name != "data" || spec.Volumes[0].MountPath != "/data" {
		return ErrOwned
	}
	return nil
}

// Cleanup keeps using duckDataMount. Serving additionally requires OS read-only access.
func validateDuckRecoveryServingMount(iid id.ID, info *containerInfo, labels map[string]string) error {
	if err := validateDuckRecoveryContract(iid, labels); err != nil {
		return err
	}
	if info == nil || info.ID == "" || info.Image == "" || info.Config.Image != labels[owned.LabelImage] || !slices.Equal(info.Config.Entrypoint, []string{"/loomplane-duckdb"}) || !slices.Equal(info.Config.Cmd, []string{"serve-readonly"}) || info.HostConfig.Privileged || len(info.HostConfig.CapAdd) != 0 || len(info.HostConfig.VolumesFrom) != 0 {
		return ErrOwned
	}
	for _, key := range []string{enginebackup.LabelEngine, enginebackup.LabelRevision, enginebackup.LabelSource, LabelRecovery, owned.LabelPublic, owned.LabelConnection, owned.LabelRevision} {
		if info.Config.Labels[key] != labels[key] {
			return ErrOwned
		}
	}
	if err := duckDataMount(info, iid); err != nil {
		return err
	}
	for _, mount := range info.Mounts {
		if strings.HasPrefix(mount.Destination, "/data/") || (mount.Destination == "/data" && (mount.RW == nil || *mount.RW)) {
			return ErrOwned
		}
	}
	return nil
}

// Census includes stopped and renamed containers. Unknown attachments are retained.
func duckStageCensus(ctx context.Context, c *Client, iid id.ID, servingID, helperID string, requireRO bool) error {
	var named containerInfo
	err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+ContainerName(iid)+"-readonly-stage/json", nil, &named)
	if !errors.Is(err, ErrMissing) && (err != nil || helperID == "" || named.ID != helperID) {
		return errors.Join(err, ErrOwned)
	}
	var inventory []struct {
		ID     string `json:"Id"`
		Labels map[string]string
		Mounts *[]containerMount
	}
	if err := dockerJSON(ctx, c, http.MethodGet, "/containers/json?all=true", nil, &inventory); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range inventory {
		if item.ID == "" || item.Mounts == nil || seen[item.ID] {
			return ErrOwned
		}
		seen[item.ID] = true
		match := item.Labels[duckStageLabel] == iid.String() || item.ID == servingID || item.ID == helperID
		for _, mount := range *item.Mounts {
			match = match || (mount.Type == "volume" && mount.Name == VolumeName(iid, "data"))
		}
		if !match {
			continue
		}
		var info containerInfo
		if err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+url.PathEscape(item.ID)+"/json", nil, &info); err != nil {
			return err
		}
		if info.ID != item.ID || (info.ID != servingID && info.ID != helperID) || (info.Config.Labels[duckStageLabel] == iid.String() && info.ID != helperID) {
			return ErrOwned
		}
		found := false
		for _, mount := range info.Mounts {
			if mount.Type != "volume" || mount.Name != VolumeName(iid, "data") {
				if info.ID == servingID && requireRO && strings.HasPrefix(mount.Destination, "/data/") {
					return ErrOwned
				}
				continue
			}
			if found || mount.Destination != "/data" || ((requireRO || info.ID == helperID) && mount.RW == nil) || (info.ID == servingID && requireRO && *mount.RW) || (info.ID == helperID && !*mount.RW) {
				return ErrOwned
			}
			found = true
		}
		if !found {
			return ErrOwned
		}
	}
	if (servingID != "" && !seen[servingID]) || (helperID != "" && !seen[helperID]) {
		return ErrOwned
	}
	return nil
}

func (r *Runtime) recoveryServing(ctx context.Context, c *Client, containerID string, iid id.ID, labels map[string]string) error {
	info, err := r.inspect(ctx, c, containerID, iid, labels)
	if err != nil {
		return err
	}
	if err := validateDuckRecoveryServingMount(iid, info, labels); err != nil {
		return err
	}
	return duckStageCensus(ctx, c, iid, info.ID, "", true)
}

func (g *duckMutation) noStage(ctx context.Context) error {
	if g.stageUnknown || g.stage != nil {
		return mutationPending(g.name)
	}
	current, err := g.runtime.inspect(ctx, g.client, ContainerName(g.iid), g.iid, g.labels)
	if err != nil && !errors.Is(err, ErrMissing) {
		return err
	}
	servingID := ""
	if current != nil {
		servingID = current.ID
	}
	return duckStageCensus(ctx, g.client, g.iid, servingID, "", false)
}

type duckStageRecord struct {
	Version                                                    int
	Mode, ReservationID, ReservationRecord, ServingID, ImageID string
	ServingLabels                                              map[string]string
	Volume                                                     instanceClassVolume
}

type duckRecoveryStage struct {
	guard             *duckMutation
	record            duckStageRecord
	name, id, encoded string
}

func (s *duckRecoveryStage) command() (entrypoint, arguments []string) {
	if s.record.Mode == "initialize" {
		return []string{"/duckdb"}, []string{"-batch", "-bail", "/data/analytics.db", "-c", "CHECKPOINT;"}
	}
	return []string{"/loomplane-readonly-stage-never-start"}, []string{}
}

func (s *duckRecoveryStage) verify(ctx context.Context) (*containerInfo, error) {
	g := s.guard
	if g.stageUnknown || s.id == "" {
		return nil, mutationPending(g.name)
	}
	for _, check := range []func(context.Context) error{g.owner, g.noPrevious} {
		if err := check(ctx); err != nil {
			return nil, err
		}
	}
	if g.record.SourceID != "" {
		if err := g.sameSource(ctx); err != nil {
			return nil, err
		}
		if err := g.sameVolume(ctx); err != nil {
			return nil, err
		}
	}
	clone, err := g.runtime.inspect(ctx, g.client, s.record.ServingID, g.iid, g.labels)
	if err != nil {
		return nil, err
	}
	if clone.ID != s.record.ServingID || clone.Image != s.record.ImageID || clone.State.Running || !maps.Equal(mutationSourceLabels(clone.Config.Labels), s.record.ServingLabels) {
		return nil, ErrOwned
	}
	if mountErr := validateDuckRecoveryServingMount(g.iid, clone, g.labels); mountErr != nil {
		return nil, mountErr
	}
	volume, err := inspectDataVolume(ctx, g.client, g.iid, g.labels)
	if err != nil || volume == nil || volume.Name != s.record.Volume.Name || volume.CreatedAt != s.record.Volume.CreatedAt || !maps.Equal(volume.Labels, s.record.Volume.Labels) {
		return nil, errors.Join(err, ErrOwned)
	}
	var info containerInfo
	if err := dockerJSON(ctx, g.client, http.MethodGet, "/containers/"+url.PathEscape(s.id)+"/json", nil, &info); err != nil {
		return nil, err
	}
	entrypoint, args := s.command()
	if info.ID != s.id || info.Name != "/"+s.name || info.Image != s.record.ImageID || info.Config.Image != s.record.ImageID || !maps.Equal(info.Config.Labels, map[string]string{duckStageLabel: g.iid.String(), duckStageRecordLabel: s.encoded}) || !slices.Equal(info.Config.Entrypoint, entrypoint) || !slices.Equal(info.Config.Cmd, args) || info.HostConfig.NetworkMode != "none" || !info.HostConfig.ReadonlyRootfs || info.HostConfig.Privileged || len(info.HostConfig.CapAdd) != 0 || len(info.HostConfig.Binds) != 0 || len(info.HostConfig.VolumesFrom) != 0 || info.HostConfig.RestartPolicy.Name != "no" || len(info.Mounts) != 1 || len(info.HostConfig.Mounts) != 1 {
		return nil, ErrOwned
	}
	mount, requested := info.Mounts[0], info.HostConfig.Mounts[0]
	if mount.Type != "volume" || mount.Name != s.record.Volume.Name || mount.Destination != "/data" || mount.RW == nil || !*mount.RW || requested.Type != "volume" || requested.Source != mount.Name || requested.Target != "/data" || requested.ReadOnly || !requested.VolumeOptions.NoCopy {
		return nil, ErrOwned
	}
	if s.record.Mode == "import" && (info.State.Running || info.State.Status != "created") {
		return nil, ErrOwned
	}
	for _, ports := range info.NetworkSettings.Ports {
		if len(ports) != 0 {
			return nil, ErrOwned
		}
	}
	if err := duckStageCensus(ctx, g.client, g.iid, s.record.ServingID, s.id, true); err != nil {
		return nil, err
	}
	return &info, nil
}

func (g *duckMutation) newStage(ctx context.Context, servingID, mode string) (*duckRecoveryStage, error) {
	if mode != "initialize" && mode != "import" {
		return nil, ErrOwned
	}
	if err := g.noStage(ctx); err != nil {
		return nil, err
	}
	for _, check := range []func(context.Context) error{g.owner, g.noPrevious} {
		if err := check(ctx); err != nil {
			return nil, err
		}
	}
	if g.record.SourceID != "" {
		if err := g.sameSource(ctx); err != nil {
			return nil, err
		}
		if err := g.sameVolume(ctx); err != nil {
			return nil, err
		}
	}
	clone, err := g.runtime.inspect(ctx, g.client, servingID, g.iid, g.labels)
	if err != nil {
		return nil, err
	}
	if clone.State.Running {
		return nil, ErrOwned
	}
	if mountErr := validateDuckRecoveryServingMount(g.iid, clone, g.labels); mountErr != nil {
		return nil, mountErr
	}
	volume, err := inspectDataVolume(ctx, g.client, g.iid, g.labels)
	if err != nil || volume == nil {
		return nil, errors.Join(err, ErrOwned)
	}
	if mode == "initialize" && (g.record.Action != "apply" || g.record.SourceID != "" || g.record.VolumeCreated != "") {
		return nil, errors.New("only a newly created recovery volume can be initialized")
	}
	if mode == "import" && (g.record.Action != "restore" || g.record.SourceID != servingID || g.record.VolumeCreated != volume.CreatedAt) {
		return nil, ErrOwned
	}
	s := &duckRecoveryStage{guard: g, name: ContainerName(g.iid) + "-readonly-stage", record: duckStageRecord{Version: 1, Mode: mode, ReservationID: g.containerID, ReservationRecord: g.encoded, ServingID: servingID, ImageID: clone.Image, ServingLabels: mutationSourceLabels(clone.Config.Labels), Volume: *volume}}
	encoded, err := json.Marshal(s.record)
	if err != nil {
		return nil, err
	}
	s.encoded = string(encoded)
	mount := volumeMount{Type: "volume", Source: volume.Name, Target: "/data"}
	mount.VolumeOptions.NoCopy = true
	entrypoint, args := s.command()
	body := map[string]any{"Image": clone.Image, "Entrypoint": entrypoint, "Cmd": args, "Env": []string{}, "Labels": map[string]string{duckStageLabel: g.iid.String(), duckStageRecordLabel: s.encoded}, "HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "RestartPolicy": map[string]string{"Name": "no"}, "Mounts": []volumeMount{mount}}}
	var created struct {
		ID string `json:"Id"`
	}
	g.stage, g.stageHold, g.stageUnknown = s, true, true
	if err := dockerJSON(ctx, g.client, http.MethodPost, "/containers/create?name="+url.QueryEscape(s.name), body, &created); err != nil {
		return nil, errors.Join(err, mutationPending(g.name))
	}
	if created.ID == "" {
		return nil, mutationPending(g.name)
	}
	s.id, g.stageUnknown = created.ID, false
	if _, err := s.verify(ctx); err != nil {
		return nil, errors.Join(err, mutationPending(g.name))
	}
	return s, nil
}

func requireEmptyDuckDirectory(ctx context.Context, c *Client, containerID string) error {
	response, err := c.Request(ctx, http.MethodGet, "/containers/"+containerID+"/archive?path=/data", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("new recovery directory could not be inspected")
	}
	tr := tar.NewReader(io.LimitReader(response.Body, 64<<10))
	header, err := tr.Next()
	if err != nil || strings.TrimSuffix(header.Name, "/") != "data" || header.Typeflag != tar.TypeDir {
		return errors.New("new recovery directory is not an empty regular directory")
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return errors.New("new recovery volume contains unexpected data")
	}
	return nil
}

func (s *duckRecoveryStage) remove(ctx context.Context) error {
	g := s.guard
	info, err := s.verify(ctx)
	if err != nil {
		return err
	}
	if info.State.Running {
		return ErrOwned
	}
	g.stageUnknown = true
	if err := dockerJSON(ctx, g.client, http.MethodDelete, "/containers/"+url.PathEscape(s.id), nil, nil); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	var gone containerInfo
	if err := dockerJSON(ctx, g.client, http.MethodGet, "/containers/"+url.PathEscape(s.id)+"/json", nil, &gone); !errors.Is(err, ErrMissing) {
		return errors.Join(err, mutationPending(g.name))
	}
	if err := duckStageCensus(ctx, g.client, g.iid, s.record.ServingID, "", true); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	g.stage, g.stageUnknown = nil, false
	return nil
}

func (g *duckMutation) initializeRecovery(ctx context.Context, servingID string) error {
	s, err := g.newStage(ctx, servingID, "initialize")
	if err != nil {
		return err
	}
	if emptyErr := requireEmptyDuckDirectory(ctx, g.client, s.id); emptyErr != nil {
		return errors.Join(emptyErr, mutationPending(g.name))
	}
	if _, verifyErr := s.verify(ctx); verifyErr != nil {
		return verifyErr
	}
	g.stageUnknown = true
	if startErr := dockerSetRunning(ctx, g.client, s.id, true); startErr != nil {
		return errors.Join(startErr, mutationPending(g.name))
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var exited struct {
		StatusCode int
		Error      *struct{ Message string }
	}
	if waitErr := dockerJSON(bounded, g.client, http.MethodPost, "/containers/"+s.id+"/wait?condition=not-running", nil, &exited); waitErr != nil {
		return errors.Join(waitErr, mutationPending(g.name))
	}
	g.stageUnknown = false
	info, err := s.verify(ctx)
	if err != nil || info.State.Running || info.State.ExitCode != exited.StatusCode {
		return errors.Join(err, ErrOwned)
	}
	if exited.StatusCode != 0 || exited.Error != nil {
		return errors.Join(errors.New("recovery initialization did not exit successfully"), s.remove(ctx))
	}
	if err := noDuckWAL(ctx, g.client, s.id); err != nil {
		return err
	}
	if _, err := duckCheckpointHash(ctx, g.client, s.id); err != nil {
		return err
	}
	if err := s.remove(ctx); err != nil {
		return err
	}
	g.stageHold = false
	return nil
}

func duckCheckpointHash(ctx context.Context, c *Client, containerID string) ([sha256.Size]byte, error) {
	hash := sha256.New()
	prefix := &duckCheckpointWriter{Writer: hash}
	err := downloadEngineFile(ctx, c, containerID, "/data/analytics.db", prefix)
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	if err != nil || len(prefix.header) != 12 || string(prefix.header[8:12]) != "DUCK" {
		return sum, errors.Join(err, errors.New("invalid staged DuckDB checkpoint"))
	}
	return sum, nil
}

type duckCheckpointWriter struct {
	io.Writer
	header []byte
}

func (w *duckCheckpointWriter) Write(raw []byte) (int, error) {
	if len(w.header) < 12 {
		w.header = append(w.header, raw[:min(len(raw), 12-len(w.header))]...)
	}
	return w.Writer.Write(raw)
}

func (g *duckMutation) importRecovery(ctx context.Context, servingID string, payload *os.File) (err error) {
	if _, seekErr := payload.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	hash := sha256.New()
	if _, copyErr := io.Copy(hash, payload); copyErr != nil {
		return copyErr
	}
	var expected [sha256.Size]byte
	copy(expected[:], hash.Sum(nil))
	if _, seekErr := payload.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	s, err := g.newStage(ctx, servingID, "import")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil && !g.stageUnknown && g.stage != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			err = errors.Join(err, s.remove(cleanup))
		}
	}()
	if _, verifyErr := s.verify(ctx); verifyErr != nil {
		return verifyErr
	}
	// An error from the joined archive/tar operation cannot prove extraction stopped.
	g.stageUnknown = true
	if uploadErr := uploadEngineFile(ctx, g.client, s.id, payload); uploadErr != nil {
		return errors.Join(uploadErr, mutationPending(g.name))
	}
	g.stageUnknown = false
	if _, verifyErr := s.verify(ctx); verifyErr != nil {
		return verifyErr
	}
	actual, err := duckCheckpointHash(ctx, g.client, s.id)
	if err != nil || actual != expected {
		return errors.Join(err, errors.New("staged recovery checkpoint differs from verified backup"))
	}
	if walErr := noDuckWAL(ctx, g.client, s.id); walErr != nil {
		return walErr
	}
	if removeErr := s.remove(ctx); removeErr != nil {
		return removeErr
	}
	g.stageHold = false
	return nil
}
