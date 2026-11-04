package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

const instanceClassLabel = "loomplane.instance-class."

func instanceClassName(iid id.ID) string { return ContainerName(iid) + "-class" }

type instanceClassVolume struct {
	Name, Driver, Scope, CreatedAt string
	Labels, Options                map[string]string
}

type classObservation struct {
	Current *containerInfo
	Volume  *instanceClassVolume
}

func observedCheckpointClass(labels map[string]string) (string, error) {
	engine, revision, rawSource := labels[enginebackup.LabelEngine], labels[enginebackup.LabelRevision], labels[enginebackup.LabelSource]
	if engine == "" && (revision != "" || rawSource != "") {
		return "", ErrOwned
	}
	if rawSource != "" {
		var source enginebackup.Identity
		if json.Unmarshal([]byte(rawSource), &source) != nil || source.Validate() != nil || source.Kind != engine || source.Revision != revision {
			return "", ErrOwned
		}
	}
	if engine == "duckdb" {
		if revision == "" {
			return "", ErrOwned
		}
		return "duckdb", nil
	}
	if labels[owned.LabelCatalogKind] == "duckdb" || labels[owned.LabelHealthChecker] == "duckdb-v1" {
		return "", ErrOwned
	}
	return "ordinary", nil
}

func checkpointClass(iid id.ID, labels map[string]string) (string, error) {
	if labels[enginebackup.LabelEngine] == "duckdb" {
		if _, err := engineIdentity(iid, labels); err != nil {
			return "", err
		}
		return "duckdb", nil
	}
	return observedCheckpointClass(labels)
}

func classLabels(iid id.ID, labels map[string]string, class string) (map[string]string, error) {
	result := map[string]string{instanceClassLabel + "schema": "1", instanceClassLabel + "class": class, instanceClassLabel + "instance": iid.String()}
	for suffix, key := range map[string]string{"org": owned.LabelOrg, "project": owned.LabelProject, "environment": owned.LabelEnvironment, "connection": owned.LabelConnection} {
		if labels[key] == "" {
			return nil, ErrOwned
		}
		result[instanceClassLabel+suffix] = labels[key]
	}
	result[instanceClassLabel+"resource"] = labels[owned.LabelService]
	result[instanceClassLabel+"recovery"] = labels[LabelRecovery]
	return result, nil
}

func validateClassVolume(volume *instanceClassVolume, name string, labels map[string]string) error {
	if volume.Name != name || volume.Driver != "local" || (volume.Scope != "" && volume.Scope != "local") || len(volume.Options) != 0 || !maps.Equal(volume.Labels, labels) {
		return ErrOwned
	}
	return nil
}

func sameClassScope(iid id.ID, actual, desired map[string]string) error {
	if actual[owned.LabelInstance] != iid.String() {
		return ErrOwned
	}
	for _, key := range []string{owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection, LabelRecovery} {
		if actual[key] != desired[key] {
			return ErrOwned
		}
	}
	if actual[owned.LabelService] != "" && actual[owned.LabelService] != desired[owned.LabelService] {
		return ErrOwned
	}
	return nil
}

// Older containers did not store service, image or architecture labels. Their
// physical image and recorded native revision still identify the source.
func validateDuckSource(iid id.ID, source *containerInfo, desired map[string]string) error {
	identity, err := engineIdentity(iid, desired)
	if err != nil {
		return err
	}
	identity.Image = source.Config.Image
	identity.Revision = source.Config.Labels[enginebackup.LabelRevision]
	if err := identity.Validate(); err != nil {
		return err
	}
	for _, key := range []string{owned.LabelService, owned.LabelImage, owned.LabelArchitecture} {
		actual := source.Config.Labels[key]
		if actual != "" && ((key == owned.LabelImage && actual != source.Config.Image) || (key != owned.LabelImage && actual != desired[key])) {
			return ErrOwned
		}
	}
	return nil
}

func inspectDataVolume(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (*instanceClassVolume, error) {
	name := VolumeName(iid, "data")
	var volume instanceClassVolume
	err := dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &volume)
	if errors.Is(err, ErrMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if volume.Name != name || volume.Driver != "local" || len(volume.Options) != 0 || volume.CreatedAt == "" || volume.Labels[owned.LabelInstance] != iid.String() || volume.Labels[labelVolumeRole] != "data" {
		return nil, ErrOwned
	}
	for _, key := range []string{owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection} {
		if labels[key] == "" || volume.Labels[key] != labels[key] {
			return nil, ErrOwned
		}
	}
	return &volume, nil
}

func (r *Runtime) observeClassBootstrap(ctx context.Context, c *Client, iid id.ID, labels map[string]string, class string) (*classObservation, error) {
	current, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil && !errors.Is(err, ErrMissing) {
		return nil, err
	}
	if current != nil {
		if scopeErr := sameClassScope(iid, current.Config.Labels, labels); scopeErr != nil {
			return nil, scopeErr
		}
		actual, classErr := observedCheckpointClass(current.Config.Labels)
		if classErr != nil || actual != class {
			return nil, ErrOwned
		}
		if class == "duckdb" {
			if sourceErr := validateDuckSource(iid, current, labels); sourceErr != nil {
				return nil, sourceErr
			}
		}
	}
	for _, suffix := range []string{"-previous", "-previous-stopped", "-checkpoint", "-mutation"} {
		var existing containerInfo
		inspectErr := dockerJSON(ctx, c, http.MethodGet, "/containers/"+ContainerName(iid)+suffix+"/json", nil, &existing)
		if !errors.Is(inspectErr, ErrMissing) {
			return nil, errors.Join(inspectErr, errors.New("instance class adoption requires drained workers and reconciliation of previous containers and reservations"))
		}
	}
	volume, err := inspectDataVolume(ctx, c, iid, labels)
	if err != nil {
		return nil, err
	}
	if current == nil && volume != nil {
		return nil, errors.New("instance class is unknown for retained data; drain workers and reconcile its ownership before adoption")
	}
	return &classObservation{Current: current, Volume: volume}, nil
}

func sameClassObservation(before, after *classObservation) bool {
	if (before.Current == nil) != (after.Current == nil) || (before.Volume == nil) != (after.Volume == nil) {
		return false
	}
	if before.Current != nil && before.Current.ID != after.Current.ID {
		return false
	}
	return before.Volume == nil || before.Volume.CreatedAt == after.Volume.CreatedAt
}

// classifyInstance establishes persistent classification before lifecycle effects.
// Upgrades must drain old workers before allowing legacy class adoption.
func (r *Runtime) classifyInstance(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (string, error) {
	class, err := checkpointClass(iid, labels)
	if err != nil {
		return "", err
	}
	expected, err := classLabels(iid, labels, class)
	if err != nil {
		return "", err
	}
	name := instanceClassName(iid)
	var anchor instanceClassVolume
	err = dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &anchor)
	if err == nil {
		return class, validateClassVolume(&anchor, name, expected)
	}
	if !errors.Is(err, ErrMissing) {
		return "", err
	}
	before, err := r.observeClassBootstrap(ctx, c, iid, labels, class)
	if err != nil {
		return "", err
	}
	err = dockerJSON(ctx, c, http.MethodPost, "/volumes/create", map[string]any{"Name": name, "Driver": "local", "Labels": expected}, &anchor)
	if err != nil {
		return "", err
	}
	if anchorErr := validateClassVolume(&anchor, name, expected); anchorErr != nil {
		return "", anchorErr
	}
	anchor = instanceClassVolume{}
	if readErr := dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &anchor); readErr != nil {
		return "", readErr
	}
	if anchorErr := validateClassVolume(&anchor, name, expected); anchorErr != nil {
		return "", anchorErr
	}
	after, err := r.observeClassBootstrap(ctx, c, iid, labels, class)
	if err != nil {
		return "", err
	}
	if !sameClassObservation(before, after) {
		return "", errors.New("instance changed during class adoption; reconcile its exact source before retrying")
	}
	return class, nil
}

const duckMutationLabel = "loomplane.duckdb-mutation"
const duckMutationRecordLabel = "loomplane.duckdb-mutation.record"

// The reservation records identities only. It has no instance discovery labels.
type duckMutationRecord struct {
	Version                                                   int    `json:"version"`
	Owner                                                     string `json:"owner"`
	Action                                                    string `json:"action"`
	Operation                                                 string `json:"operation"`
	Org, Project, Environment, Connection, Instance, Resource string
	SourceID, SourceImage, SourceDockerImage, SourceRevision  string
	SourceRunning                                             bool
	SourceLabels                                              map[string]string
	DesiredImage, DesiredRevision                             string
	DesiredRunning                                            *bool `json:",omitempty"`
	Recovery                                                  string
	Volume, VolumeCreated                                     string
}

type duckMutationIntent struct {
	PrepareImage                       func(context.Context) error
	Action, Operation, Image, Revision string
	Running                            *bool
}

type duckMutation struct {
	runtime                    *Runtime
	client                     *Client
	iid                        id.ID
	labels                     map[string]string
	name, containerID, encoded string
	record                     duckMutationRecord
}

func mutationPending(name string) error {
	return fmt.Errorf("DuckDB operation is reserved at %s; drain this target's workers and follow the host recovery procedure before retrying", name)
}

func mutationSourceLabels(labels map[string]string) map[string]string {
	result := map[string]string{}
	for _, key := range []string{owned.LabelInstance, owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection, owned.LabelService, owned.LabelRevision, owned.LabelImage, owned.LabelArchitecture, owned.LabelPublic, LabelRecovery, enginebackup.LabelEngine, enginebackup.LabelRevision, enginebackup.LabelSource} {
		result[key] = labels[key]
	}
	return result
}

func (r *Runtime) acquireDuckMutation(ctx context.Context, c *Client, iid id.ID, labels map[string]string, intent duckMutationIntent) (*duckMutation, error) {
	class, err := r.classifyInstance(ctx, c, iid, labels)
	if err != nil {
		return nil, err
	}
	source, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil && !errors.Is(err, ErrMissing) {
		return nil, err
	}
	if source != nil {
		if scopeErr := sameClassScope(iid, source.Config.Labels, labels); scopeErr != nil {
			return nil, scopeErr
		}
		actual, classErr := observedCheckpointClass(source.Config.Labels)
		if classErr != nil || actual != class {
			return nil, ErrOwned
		}
	}
	if class != "duckdb" {
		return nil, nil
	}
	if intent.Action == "apply" && intent.Image != labels[owned.LabelImage] {
		return nil, ErrOwned
	}
	if source != nil {
		if source.ID == "" {
			return nil, ErrOwned
		}
		if sourceErr := validateDuckSource(iid, source, labels); sourceErr != nil {
			return nil, sourceErr
		}
		if intent.Action != "apply" {
			if source.Config.Image != labels[owned.LabelImage] {
				return nil, ErrOwned
			}
			for _, key := range []string{owned.LabelRevision, owned.LabelPublic, enginebackup.LabelRevision, enginebackup.LabelSource} {
				if source.Config.Labels[key] != labels[key] {
					return nil, ErrOwned
				}
			}
		}
	}
	if (intent.Action == "backup" || intent.Action == "restore" || intent.Action == "set-running") && source == nil {
		return nil, ErrMissing
	}
	if (intent.Action == "backup" || intent.Action == "restore") && !source.State.Running {
		return nil, errors.New("native checkpoint source is no longer running")
	}
	if intent.Action == "backup" && labels[LabelRecovery] != "" {
		return nil, ErrOwned
	}
	if intent.Action == "purge" || intent.Action == "restore" {
		if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
			return nil, ErrOwned
		}
	}
	if intent.Action == "restore" {
		var original enginebackup.Identity
		if json.Unmarshal([]byte(labels[enginebackup.LabelSource]), &original) != nil || original.Instance == iid.String() || original.Validate() != nil {
			return nil, ErrOwned
		}
	}
	var legacy containerInfo
	if legacyErr := dockerJSON(ctx, c, http.MethodGet, "/containers/"+ContainerName(iid)+"-checkpoint/json", nil, &legacy); !errors.Is(legacyErr, ErrMissing) {
		return nil, errors.Join(legacyErr, errors.New("legacy checkpoint reservation needs drained host reconciliation"))
	}
	volume, err := inspectDataVolume(ctx, c, iid, labels)
	if err != nil {
		return nil, err
	}
	if source != nil {
		if volume == nil {
			return nil, ErrOwned
		}
		if mountErr := duckDataMount(source, iid); mountErr != nil {
			return nil, mountErr
		}
	}
	record := duckMutationRecord{Version: 1, Owner: rand.Text(), Action: intent.Action, Operation: intent.Operation, Org: labels[owned.LabelOrg], Project: labels[owned.LabelProject], Environment: labels[owned.LabelEnvironment], Connection: labels[owned.LabelConnection], Instance: iid.String(), Resource: labels[owned.LabelService], DesiredImage: intent.Image, DesiredRevision: intent.Revision, DesiredRunning: intent.Running, Recovery: labels[LabelRecovery], Volume: VolumeName(iid, "data")}
	image := labels[owned.LabelImage]
	if source != nil {
		record.SourceID, record.SourceImage, record.SourceRevision, record.SourceRunning = source.ID, source.Config.Image, source.Config.Labels[owned.LabelRevision], source.State.Running
		record.SourceLabels = mutationSourceLabels(source.Config.Labels)
		record.SourceDockerImage = source.Image
		image = source.Image
		if image == "" {
			image = source.Config.Image
		}
	}
	if volume != nil {
		record.VolumeCreated = volume.CreatedAt
	}
	// Existing instances already pin a local image. Docker create never pulls;
	// using its image ID also works when an operator removed the repository tag.
	if source == nil && intent.Action == "apply" {
		if intent.PrepareImage == nil {
			return nil, errors.New("new DuckDB Apply requires its authenticated image preparation")
		}
		if prepareErr := intent.PrepareImage(ctx); prepareErr != nil {
			return nil, prepareErr
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	guard := &duckMutation{runtime: r, client: c, iid: iid, labels: maps.Clone(labels), name: ContainerName(iid) + "-mutation", record: record, encoded: string(encoded)}
	body := map[string]any{"Image": image, "Entrypoint": []string{"/loomplane-reservation-never-start"}, "Labels": map[string]string{duckMutationLabel: "1", duckMutationRecordLabel: guard.encoded}, "HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true}}
	var created struct {
		ID string `json:"Id"`
	}
	if err := dockerJSON(ctx, c, http.MethodPost, "/containers/create?name="+url.QueryEscape(guard.name), body, &created); err != nil {
		return nil, errors.Join(err, mutationPending(guard.name))
	}
	guard.containerID = created.ID
	if created.ID == "" {
		return nil, mutationPending(guard.name)
	}
	// Every failure after the atomic claim keeps the reservation for reconciliation.
	for _, check := range []func(context.Context) error{guard.owner, guard.sameSourceState, guard.sameVolume, guard.noPrevious} {
		if err := check(ctx); err != nil {
			return nil, errors.Join(err, mutationPending(guard.name))
		}
	}
	return guard, nil
}

func duckDataMount(source *containerInfo, iid id.ID) error {
	found := false
	for _, mount := range source.Mounts {
		if mount.Destination == "/data" {
			if found || mount.Type != "volume" || mount.Name != VolumeName(iid, "data") {
				return ErrOwned
			}
			found = true
		} else if mount.Name == VolumeName(iid, "data") {
			return ErrOwned
		}
	}
	if !found {
		return ErrOwned
	}
	return nil
}

func (g *duckMutation) sameSource(ctx context.Context) error {
	source, err := g.runtime.inspect(ctx, g.client, ContainerName(g.iid), g.iid, g.labels)
	if g.record.SourceID == "" {
		if errors.Is(err, ErrMissing) {
			return nil
		}
		return ErrOwned
	}
	if err != nil {
		return err
	}
	if source.ID != g.record.SourceID || source.Config.Image != g.record.SourceImage || source.Image != g.record.SourceDockerImage || !maps.Equal(mutationSourceLabels(source.Config.Labels), g.record.SourceLabels) {
		return ErrOwned
	}
	return duckDataMount(source, g.iid)
}

func (g *duckMutation) sameSourceState(ctx context.Context) error {
	if err := g.sameSource(ctx); err != nil {
		return err
	}
	if g.record.SourceID == "" {
		return nil
	}
	current, err := g.runtime.inspect(ctx, g.client, g.record.SourceID, g.iid, g.labels)
	if err != nil {
		return err
	}
	if current.State.Running != g.record.SourceRunning {
		return ErrOwned
	}
	return nil
}

func (g *duckMutation) sameVolume(ctx context.Context) error {
	volume, err := inspectDataVolume(ctx, g.client, g.iid, g.labels)
	if err != nil {
		return err
	}
	if g.record.VolumeCreated == "" {
		if volume == nil {
			return nil
		}
		return ErrOwned
	}
	if volume == nil || volume.CreatedAt != g.record.VolumeCreated {
		return ErrOwned
	}
	return nil
}

func (g *duckMutation) noPrevious(ctx context.Context) error {
	for _, suffix := range []string{"-previous", "-previous-stopped", "-checkpoint"} {
		var info containerInfo
		err := dockerJSON(ctx, g.client, http.MethodGet, "/containers/"+ContainerName(g.iid)+suffix+"/json", nil, &info)
		if !errors.Is(err, ErrMissing) {
			return errors.Join(err, errors.New("previous DuckDB container or checkpoint needs reconciliation"))
		}
	}
	return nil
}

func (g *duckMutation) owner(ctx context.Context) error {
	expected, err := classLabels(g.iid, g.labels, "duckdb")
	if err != nil {
		return err
	}
	var anchor instanceClassVolume
	if err := dockerJSON(ctx, g.client, http.MethodGet, "/volumes/"+instanceClassName(g.iid), nil, &anchor); err != nil {
		return err
	}
	if anchorErr := validateClassVolume(&anchor, instanceClassName(g.iid), expected); anchorErr != nil {
		return anchorErr
	}
	var info struct {
		containerInfo
		Name       string
		HostConfig struct {
			NetworkMode    string
			ReadonlyRootfs bool
		}
	}
	if err := dockerJSON(ctx, g.client, http.MethodGet, "/containers/"+g.containerID+"/json", nil, &info); err != nil {
		return err
	}
	expectedLabels := map[string]string{duckMutationLabel: "1", duckMutationRecordLabel: g.encoded}
	if info.ID != g.containerID || info.Name != "/"+g.name || info.State.Running || len(info.Mounts) != 0 || !maps.Equal(info.Config.Labels, expectedLabels) || info.HostConfig.NetworkMode != "none" || !info.HostConfig.ReadonlyRootfs {
		return ErrOwned
	}
	for _, ports := range info.NetworkSettings.Ports {
		if len(ports) != 0 {
			return ErrOwned
		}
	}
	return nil
}

func (g *duckMutation) finish(ctx context.Context, operationErr error) error {
	if g == nil {
		return nil
	}
	if operationErr != nil {
		return mutationPending(g.name)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := g.owner(cleanup); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	if err := g.terminal(cleanup); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	if err := g.release(cleanup); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	return nil
}

func (g *duckMutation) terminal(ctx context.Context) error {
	if err := g.noPrevious(ctx); err != nil {
		return err
	}
	current, err := g.runtime.inspect(ctx, g.client, ContainerName(g.iid), g.iid, g.labels)
	switch g.record.Action {
	case "remove", "purge":
		if !errors.Is(err, ErrMissing) {
			return ErrOwned
		}
		if g.record.SourceID != "" {
			var original containerInfo
			if originalErr := dockerJSON(ctx, g.client, http.MethodGet, "/containers/"+g.record.SourceID+"/json", nil, &original); !errors.Is(originalErr, ErrMissing) {
				return ErrOwned
			}
		}
		if g.record.Action == "remove" {
			return g.sameVolume(ctx)
		}
		var volume any
		if volumeErr := dockerJSON(ctx, g.client, http.MethodGet, "/volumes/"+g.record.Volume, nil, &volume); !errors.Is(volumeErr, ErrMissing) {
			return ErrOwned
		}
		return nil
	case "apply":
		if err != nil {
			return err
		}
		if g.record.SourceID != "" && current.ID != g.record.SourceID {
			var original containerInfo
			if originalErr := dockerJSON(ctx, g.client, http.MethodGet, "/containers/"+g.record.SourceID+"/json", nil, &original); !errors.Is(originalErr, ErrMissing) {
				return ErrOwned
			}
		}
		if current.ID == "" || current.Config.Image != g.record.DesiredImage || current.Config.Labels[owned.LabelRevision] != g.record.DesiredRevision || current.Config.Labels[enginebackup.LabelEngine] != "duckdb" || !current.State.Running {
			return ErrOwned
		}
		if scopeErr := sameClassScope(g.iid, current.Config.Labels, g.labels); scopeErr != nil {
			return scopeErr
		}
		if sourceErr := validateDuckSource(g.iid, current, g.labels); sourceErr != nil {
			return sourceErr
		}
		if mountErr := duckDataMount(current, g.iid); mountErr != nil {
			return mountErr
		}
		if g.record.VolumeCreated != "" {
			if volumeErr := g.sameVolume(ctx); volumeErr != nil {
				return volumeErr
			}
		} else {
			volume, volumeErr := inspectDataVolume(ctx, g.client, g.iid, g.labels)
			if volumeErr != nil {
				return volumeErr
			}
			if volume == nil {
				return ErrOwned
			}
		}
	default:
		if err != nil {
			return err
		}
		if err := g.sameSource(ctx); err != nil {
			return err
		}
		if err := g.sameVolume(ctx); err != nil {
			return err
		}
		desired := true
		if g.record.DesiredRunning != nil {
			desired = *g.record.DesiredRunning
		}
		if current.State.Running != desired {
			return ErrOwned
		}
		if !desired {
			return nil
		}
	}
	return g.waitAuthenticated(ctx, current.ID)
}

func (g *duckMutation) release(ctx context.Context) error {
	if err := g.owner(ctx); err != nil {
		return err
	}
	return dockerJSON(ctx, g.client, http.MethodDelete, "/containers/"+g.containerID, nil, nil)
}

// Native authentication is mandatory even when caller health metadata is absent.
func (g *duckMutation) waitAuthenticated(ctx context.Context, containerID string) error {
	labels := maps.Clone(g.labels)
	labels[owned.LabelCatalogKind] = "duckdb"
	labels[owned.LabelHealthChecker] = "duckdb-v1"
	return g.runtime.wait(ctx, g.client, containerID, g.iid, labels)
}

// discardRestore removes only the exact private destination of a failed import.
func (g *duckMutation) discardRestore(ctx context.Context) error {
	if g.record.Action != "restore" || g.record.Recovery == "" || g.labels[owned.LabelPublic] != "false" || g.record.SourceID == "" || g.record.VolumeCreated == "" {
		return ErrOwned
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, check := range []func(context.Context) error{g.owner, g.sameSource, g.sameVolume, g.noPrevious} {
		if err := check(cleanup); err != nil {
			return errors.Join(err, mutationPending(g.name))
		}
	}
	// Bypass the public entry point while retaining this operation's reservation.
	if err := g.runtime.removeRuntime(cleanup, g.client, g.iid, g.labels); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	if err := g.owner(cleanup); err != nil {
		return err
	}
	if err := g.sameVolume(cleanup); err != nil {
		return err
	}
	if err := dockerJSON(cleanup, g.client, http.MethodDelete, "/volumes/"+g.record.Volume, nil, nil); err != nil {
		return errors.Join(err, mutationPending(g.name))
	}
	for _, name := range []string{ContainerName(g.iid), g.record.SourceID} {
		var info containerInfo
		if err := dockerJSON(cleanup, g.client, http.MethodGet, "/containers/"+name+"/json", nil, &info); !errors.Is(err, ErrMissing) {
			return ErrOwned
		}
	}
	if err := g.noPrevious(cleanup); err != nil {
		return err
	}
	var volume any
	if err := dockerJSON(cleanup, g.client, http.MethodGet, "/volumes/"+g.record.Volume, nil, &volume); !errors.Is(err, ErrMissing) {
		return ErrOwned
	}
	return g.release(cleanup)
}

// ordinaryEngineSource rejects contradictory Duck metadata before a legacy
// database session can expose native commands, uploads or protocol access.
// Shared inspection remains available to status and read-only callers.
func (r *Runtime) ordinaryEngineSource(ctx context.Context, c *Client, iid id.ID, labels map[string]string) (*containerInfo, error) {
	source, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil {
		return nil, err
	}
	class, err := observedCheckpointClass(source.Config.Labels)
	if err != nil || class == "duckdb" {
		return nil, ErrOwned
	}
	return source, nil
}
