package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const labelVolumeRole = owned.LabelVolumeRole

type volumeMount struct {
	Type          string
	Source        string
	Target        string
	VolumeOptions struct{ NoCopy bool }
}

// VolumeName stays the same across runtime revisions. A different instance
// gets different storage, including when restoring into a fresh instance.
func VolumeName(iid id.ID, role string) string {
	return owned.PersistentVolumeName(iid, role)
}

func validateVolumes(volumes []cpprovider.VolumeSpec) error {
	if err := owned.ValidatePersistentVolumes(volumes); err != nil {
		return err
	}
	for _, v := range volumes {
		if v.SizeMB != 0 {
			return errors.New("docker local volumes use shared host capacity; size quotas are unsupported")
		}
	}
	return nil
}

func ensureVolumes(ctx context.Context, c *Client, iid id.ID, volumes []cpprovider.VolumeSpec, labels map[string]string) ([]volumeMount, error) {
	mounts := make([]volumeMount, 0, len(volumes))
	for _, v := range volumes {
		expected := map[string]string{owned.LabelInstance: iid.String(), labelVolumeRole: v.Name}
		for _, key := range []string{owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment, owned.LabelConnection} {
			if labels[key] == "" {
				return nil, errors.New("persistent volume needs complete ownership")
			}
			expected[key] = labels[key]
		}
		name := VolumeName(iid, v.Name)
		var volume struct {
			Name, Driver    string
			Labels, Options map[string]string
		}
		err := dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &volume)
		if errors.Is(err, ErrMissing) {
			if createErr := dockerJSON(ctx, c, http.MethodPost, "/volumes/create", map[string]any{"Name": name, "Driver": "local", "Labels": expected}, nil); createErr != nil {
				return nil, createErr
			}
			// Docker create is idempotent. Read back to detect a concurrent collision.
			err = dockerJSON(ctx, c, http.MethodGet, "/volumes/"+name, nil, &volume)
		}
		if err != nil {
			return nil, err
		}
		if volume.Name != name || volume.Driver != "local" || len(volume.Options) > 0 {
			return nil, ErrOwned
		}
		for key, value := range expected {
			if volume.Labels[key] != value {
				return nil, ErrOwned
			}
		}
		m := volumeMount{Type: "volume", Source: name, Target: v.MountPath}
		m.VolumeOptions.NoCopy = true
		mounts = append(mounts, m)
	}
	return mounts, nil
}

// A rollout cannot silently switch data directories or adopt an image volume.
func checkVolumeLayout(existing *containerInfo, iid id.ID, volumes []cpprovider.VolumeSpec) error {
	expected := map[string]string{}
	for _, v := range volumes {
		expected[v.MountPath] = VolumeName(iid, v.Name)
	}
	for _, m := range existing.Mounts {
		if m.Type == "volume" && strings.HasPrefix(m.Name, "lpv-") {
			if expected[m.Destination] != m.Name {
				return errors.New("persistent volume layout changed; migrate into a fresh service")
			}
		}
		if name, ok := expected[m.Destination]; ok {
			if m.Type != "volume" || m.Name != name {
				return ErrOwned
			}
			delete(expected, m.Destination)
		}
	}
	if len(expected) > 0 {
		return errors.New("persistent volume layout changed; migrate into a fresh service")
	}
	return nil
}
