package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// PurgeRecovery destroys only the temporary recovery instance's owned volume.
func (r *Runtime) PurgeRecovery(ctx context.Context, iid id.ID, labels map[string]string) (err error) {
	if labels[LabelRecovery] == "" || labels[owned.LabelPublic] != "false" {
		return ErrOwned
	}
	c, err := r.Session(ctx, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	guard, err := r.acquireDuckMutation(ctx, c, iid, labels, duckMutationIntent{Action: "purge"})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.finish(ctx, err)) }()
	info, err := r.inspect(ctx, c, ContainerName(iid), iid, labels)
	if err != nil && !errors.Is(err, ErrMissing) {
		return err
	}
	if err == nil && info.Config.Labels[LabelRecovery] != labels[LabelRecovery] {
		return ErrOwned
	}
	// Check the volume owner before removing anything. Missing storage is idempotent.
	name := VolumeName(iid, "data")
	var volume struct{ Labels map[string]string }
	volumeErr := dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &volume)
	if volumeErr != nil && !errors.Is(volumeErr, ErrMissing) {
		return volumeErr
	}
	if volumeErr == nil {
		expected := map[string]string{owned.LabelInstance: iid.String(), labelVolumeRole: "data"}
		for _, key := range []string{owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection} {
			if labels[key] == "" {
				return ErrOwned
			}
			expected[key] = labels[key]
		}
		for key, value := range expected {
			if volume.Labels[key] != value {
				return ErrOwned
			}
		}
	}
	if checkErr := r.removeRuntime(ctx, c, iid, labels); checkErr != nil {
		return checkErr
	}
	if volumeErr == nil {
		if guard != nil {
			if ownerErr := guard.owner(ctx); ownerErr != nil {
				return ownerErr
			}
			if checkErr := guard.noStage(ctx); checkErr != nil {
				return checkErr
			}
			if identityErr := guard.sameVolume(ctx); identityErr != nil {
				return identityErr
			}
		}
		err = dockerJSON(ctx, c, http.MethodDelete, "/volumes/"+name, nil, nil)
		if !errors.Is(err, ErrMissing) {
			return err
		}
	}
	return nil
}
